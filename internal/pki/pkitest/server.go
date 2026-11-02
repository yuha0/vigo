package pkitest

import (
	"crypto/tls"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/yuha0/vigo/internal/pki"
)

// Sources issues a leaf for each identity from one new CA and loads them.
func Sources(t testing.TB, ids ...string) map[string]*pki.Source {
	t.Helper()
	ca, err := NewCA("test")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]*pki.Source{}
	for i, id := range ids {
		leaf, err := ca.Issue(id, time.Hour, []string{"localhost"}, []net.IP{net.IPv4(127, 0, 0, 1)})
		if err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(t.TempDir(), string(rune('a'+i)))
		if err := WriteDir(dir, ca, leaf); err != nil {
			t.Fatal(err)
		}
		src, err := pki.Load(dir)
		if err != nil {
			t.Fatal(err)
		}
		out[id] = src
	}
	return out
}

// ServeTLS serves h over mTLS with src's identity, authenticating clients
// with pki.Authenticate, and returns its https URL. The listener is wrapped
// directly: httptest's StartTLS would add its own certificate, which Go
// prefers over GetCertificate when the client sends no SNI.
func ServeTLS(t testing.TB, src *pki.Source, allow func(string) bool, h http.Handler) string {
	t.Helper()
	srv := httptest.NewUnstartedServer(pki.Authenticate(h))
	srv.Listener = tls.NewListener(srv.Listener, src.ServerTLS(allow))
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.Start()
	t.Cleanup(srv.Close)
	return "https://" + srv.Listener.Addr().String()
}

// Client presents src's identity and expects a server with identity want,
// over HTTP/2.
func Client(src *pki.Source, want string) *http.Client {
	var p http.Protocols
	p.SetHTTP2(true)
	return &http.Client{Transport: &http.Transport{TLSClientConfig: src.ClientTLS(want), Protocols: &p}}
}
