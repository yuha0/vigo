package pki_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yuha0/vigo/internal/pki"
	"github.com/yuha0/vigo/internal/pki/pkitest"
)

// secretVolume lays files out like a Kubernetes Secret volume: the names
// are symlinks into ..data, itself a symlink to a timestamped directory, and
// an update atomically repoints ..data.
type secretVolume struct {
	t   *testing.T
	dir string
	gen int
}

func newSecretVolume(t *testing.T) *secretVolume {
	v := &secretVolume{t: t, dir: t.TempDir()}
	for _, name := range []string{"tls.crt", "tls.key", "ca.crt"} {
		if err := os.Symlink(filepath.Join("..data", name), filepath.Join(v.dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	return v
}

func (v *secretVolume) write(ca *pkitest.CA, leaf *pkitest.Leaf) {
	v.t.Helper()
	v.gen++
	ts := filepath.Join(v.dir, "..gen"+strings.Repeat("x", v.gen))
	if err := pkitest.WriteDir(ts, ca, leaf); err != nil {
		v.t.Fatal(err)
	}
	tmp := filepath.Join(v.dir, "..data_tmp")
	if err := os.Symlink(filepath.Base(ts), tmp); err != nil {
		v.t.Fatal(err)
	}
	if err := os.Rename(tmp, filepath.Join(v.dir, "..data")); err != nil {
		v.t.Fatal(err)
	}
}

func issue(t *testing.T, ca *pkitest.CA, id string) *pkitest.Leaf {
	t.Helper()
	leaf, err := ca.Issue(id, time.Hour, []string{"localhost"}, []net.IP{net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	return leaf
}

func newCA(t *testing.T) *pkitest.CA {
	t.Helper()
	ca, err := pkitest.NewCA("test")
	if err != nil {
		t.Fatal(err)
	}
	return ca
}

func dirWith(t *testing.T, ca *pkitest.CA, id string) string {
	t.Helper()
	dir := t.TempDir()
	if err := pkitest.WriteDir(dir, ca, issue(t, ca, id)); err != nil {
		t.Fatal(err)
	}
	return dir
}

func load(t *testing.T, dir string, opts ...pki.Option) *pki.Source {
	t.Helper()
	s, err := pki.Load(dir, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// serve starts an mTLS server answering with the caller's identity.
func serve(t *testing.T, src *pki.Source, allow func(string) bool) string {
	t.Helper()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := pki.PeerIdentity(r)
		io.WriteString(w, id)
	}))
	srv.Listener = tls.NewListener(srv.Listener, src.ServerTLS(allow))
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.Start()
	t.Cleanup(srv.Close)
	return "https://" + srv.Listener.Addr().String()
}

func get(src *pki.Source, want, url string) (string, error) {
	c := &http.Client{Transport: &http.Transport{TLSClientConfig: src.ClientTLS(want)}}
	resp, err := c.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return string(b), err
}

func TestMutualTLSAuthorization(t *testing.T) {
	ca := newCA(t)
	other := newCA(t)
	core := load(t, dirWith(t, ca, pki.Core))
	home := load(t, dirWith(t, ca, pki.IntegrationIdentity("homekit")))
	rogueHome := load(t, dirWith(t, other, pki.IntegrationIdentity("homekit")))
	rogueAdmin := load(t, dirWith(t, ca, "spiffe://vigo/admin"))

	allowAll := func(id string) bool {
		_, ok := pki.IntegrationKind(id)
		return ok || id == pki.Core
	}
	coreURL := serve(t, core, allowAll)

	tests := []struct {
		name    string
		client  *pki.Source
		want    string
		wantErr bool
		wantID  string
	}{
		{"integration calls core", home, pki.Core, false, "spiffe://vigo/integration/homekit"},
		{"core calls core", core, pki.Core, false, pki.Core},
		{"client from another CA", rogueHome, pki.Core, true, ""},
		{"identity not allowed", rogueAdmin, pki.Core, true, ""},
		{"server has the wrong identity", home, pki.IntegrationIdentity("homekit"), true, ""},
	}
	for _, tt := range tests {
		got, err := get(tt.client, tt.want, coreURL)
		if (err != nil) != tt.wantErr {
			t.Errorf("%s: err = %v", tt.name, err)
			continue
		}
		if got != tt.wantID {
			t.Errorf("%s: server saw %q, want %q", tt.name, got, tt.wantID)
		}
	}

	if _, err := http.Get(coreURL); err == nil {
		t.Error("plain client without certificate accepted")
	}
	plain := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	if _, err := plain.Get(coreURL); err == nil {
		t.Error("TLS client without certificate accepted")
	}
}

func TestReloadAfterSecretVolumeSwap(t *testing.T) {
	ca := newCA(t)
	vol := newSecretVolume(t)
	vol.write(ca, issue(t, ca, pki.Core))

	var loaded atomic.Int32
	src := load(t, vol.dir, pki.OnLoad(func(*x509.Certificate) { loaded.Add(1) }))
	first := src.Leaf().SerialNumber

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go src.Watch(ctx, 10*time.Millisecond)

	homeDir := dirWith(t, ca, pki.IntegrationIdentity("homekit"))
	url := serve(t, src, func(string) bool { return true })
	home := load(t, homeDir)
	if _, err := get(home, pki.Core, url); err != nil {
		t.Fatal(err)
	}

	// A renewal with a new CA: the old client must be refused once the
	// server reloads, without restarting the listener.
	ca2 := newCA(t)
	vol.write(ca2, issue(t, ca2, pki.Core))
	deadline := time.Now().Add(3 * time.Second)
	for src.Leaf().SerialNumber.Cmp(first) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("certificate not reloaded")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if n := loaded.Load(); n != 2 {
		t.Errorf("OnLoad called %d times, want 2", n)
	}
	if _, err := get(home, pki.Core, url); err == nil {
		t.Error("client from the replaced CA still accepted")
	}
	home2Dir := dirWith(t, ca2, pki.IntegrationIdentity("homekit"))
	if _, err := get(load(t, home2Dir), pki.Core, url); err != nil {
		t.Errorf("client from the new CA: %v", err)
	}
}

func TestReloadKeepsOldMaterialOnError(t *testing.T) {
	ca := newCA(t)
	dir := dirWith(t, ca, pki.Core)
	src := load(t, dir)
	serialBefore := src.Leaf().SerialNumber
	os.WriteFile(filepath.Join(dir, "tls.key"), []byte("garbage"), 0o600)
	if _, err := src.Reload(); err == nil {
		t.Fatal("reload of a broken key succeeded")
	}
	if src.Leaf().SerialNumber.Cmp(serialBefore) != 0 {
		t.Error("material replaced despite the error")
	}
	if changed, err := src.Reload(); err == nil || changed {
		t.Errorf("changed=%v err=%v", changed, err)
	}
}

func TestLoadRequiresSpiffeID(t *testing.T) {
	ca := newCA(t)
	leaf, err := ca.Issue("https://example.com/x", time.Hour, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	pkitest.WriteDir(dir, ca, leaf)
	if _, err := pki.Load(dir); err == nil {
		t.Error("certificate without spiffe SAN accepted")
	}
	if _, err := pki.Load(t.TempDir()); err == nil {
		t.Error("empty directory accepted")
	}
}

func TestIntegrationKind(t *testing.T) {
	tests := map[string]string{
		"spiffe://vigo/integration/homekit": "homekit",
		"spiffe://vigo/integration/matter":  "matter",
		"spiffe://vigo/integration/":        "",
		"spiffe://vigo/integration/a/b":     "",
		"spiffe://vigo/integration/Home":    "",
		"spiffe://vigo/core":                "",
		"spiffe://other/integration/x":      "",
	}
	for id, want := range tests {
		got, ok := pki.IntegrationKind(id)
		if got != want || ok != (want != "") {
			t.Errorf("IntegrationKind(%q) = %q, %v", id, got, ok)
		}
	}
}
