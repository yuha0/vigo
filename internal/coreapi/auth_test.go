package coreapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/yuha0/vigo/internal/auth"
	"github.com/yuha0/vigo/internal/db/dbtest"
	"github.com/yuha0/vigo/internal/metrics"
	"github.com/yuha0/vigo/internal/pki"
	"github.com/yuha0/vigo/internal/pki/pkitest"
)

var fastArgon = auth.Params{MemoryKiB: 1024, Time: 1, Threads: 1, SaltLen: 16, KeyLen: 32}

type httpReply struct {
	status int
	body   map[string]any
	header http.Header
}

func send(t *testing.T, method, url, body, cookie, origin string) httpReply {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	if method == "POST" {
		req.Header.Set("Content-Type", "application/json")
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: cookie})
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	r := httpReply{status: resp.StatusCode, header: resp.Header}
	json.Unmarshal(b, &r.body)
	return r
}

func TestPublicListenerRequiresASession(t *testing.T) {
	dbe := dbtest.Open(t)
	web := fstest.MapFS{
		"index.html":           {Data: []byte("<html>vigo</html>")},
		"assets/index-abc1.js": {Data: []byte("js")},
	}
	e := newEnv(t, envOpts{web: web, tweak: func(o *Options) {
		o.Auth = auth.New(auth.Options{Pool: dbe.DB.Pool(), Metrics: metrics.New(), Params: &fastArgon})
		o.HomeEndpoint = func(context.Context) (string, error) { return "https://127.0.0.1:1", nil }
		o.HomeTLS = pkitest.Sources(t, pki.Core)[pki.Core].ClientTLS(pki.IntegrationIdentity("homekit"))
	}})
	origin := e.url

	protected := []struct{ method, path string }{
		{"POST", "/vigo.core.v1.TVService/GetStatus"},
		{"POST", "/vigo.core.v1.TVService/WatchEvents"},
		{"POST", "/vigo.core.v1.ArtService/ListArt"},
		{"POST", "/vigo.core.v1.PairingService/StartPairing"},
		{"POST", "/vigo.core.v1.ConfigService/GetConfig"},
		{"POST", "/vigo.core.v1.LibraryService/ListArtworks"},
		{"POST", "/vigo.core.v1.AuthService/Logout"},
		{"POST", "/vigo.core.v1.AuthService/ChangePassword"},
		{"GET", "/art/SAM-S2701/thumbnail"},
		{"GET", "/library/tv:SAM-S2701/thumbnail"},
		{"GET", "/library/tv:SAM-S2701/image"},
		{"POST", "/sources/art/files"},
		{"POST", "/vigo.home.v1.HomeService/GetProvider"},
	}
	for _, p := range protected {
		r := send(t, p.method, e.url+p.path, "{}", "", origin)
		// Logout and ChangePassword answer through AuthService itself.
		if r.status != 401 || r.body["code"] != "unauthenticated" {
			t.Errorf("%s %s without a session: %d %v", p.method, p.path, r.status, r.body)
		}
		if r := send(t, p.method, e.url+p.path, "{}", "forged-token", origin); r.status != 401 {
			t.Errorf("%s %s with a forged session: %d", p.method, p.path, r.status)
		}
	}

	for _, path := range []string{"/", "/gallery", "/settings/tv", "/assets/index-abc1.js"} {
		if r := send(t, "GET", e.url+path, "", "", ""); r.status != 200 {
			t.Errorf("GET %s: %d", path, r.status)
		}
	}
	if r := send(t, "POST", e.url+"/vigo.core.v1.AuthService/GetAuthState", "{}", "", origin); r.status != 200 || r.body["passwordSet"] == true {
		t.Errorf("GetAuthState: %d %v", r.status, r.body)
	}
	if r := send(t, "POST", e.url+"/vigo.core.v1.AuthService/Login", `{"password":"anything at all"}`, "", origin); r.body["code"] != "unauthenticated" {
		t.Errorf("Login before setup: %v", r.body)
	}

	setup := send(t, "POST", e.url+"/vigo.core.v1.AuthService/SetupPassword", `{"password":"correct horse battery"}`, "", origin)
	var token string
	for _, c := range (&http.Response{Header: setup.header}).Cookies() {
		if c.Name == auth.CookieName {
			token = c.Value
		}
	}
	if setup.status != 200 || token == "" {
		t.Fatalf("setup: %d %v", setup.status, setup.body)
	}
	if r := send(t, "POST", e.url+"/vigo.core.v1.TVService/GetStatus", "{}", token, origin); r.status != 200 || r.body["status"] == nil {
		t.Errorf("GetStatus with a session: %d %v", r.status, r.body)
	}
	if r := send(t, "GET", e.url+"/art/SAM-S2701/thumbnail", "", token, ""); r.status != 200 {
		t.Errorf("thumbnail with a session: %d", r.status)
	}
	if r := send(t, "POST", e.url+"/vigo.core.v1.TVService/GetStatus", "{}", token, "https://evil.example"); r.status != 403 {
		t.Errorf("cross-origin with a session: %d", r.status)
	}
	if r := send(t, "GET", e.opsURL+"/metrics", "", "", ""); r.status != 200 {
		t.Errorf("ops listener needs a session: %d", r.status)
	}
}

// TestForwardKeepsCookies: a standby forwards the session cookie to the
// leader and passes the leader's Set-Cookie back.
func TestForwardKeepsCookies(t *testing.T) {
	src := pkitest.Sources(t, pki.Core)
	var seen string
	leader := pkitest.ServeTLS(t, src[pki.Core], AllowInternal, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(auth.CookieName); err == nil {
			seen = c.Value
		}
		http.SetCookie(w, &http.Cookie{Name: "from_leader", Value: "1", Path: "/"})
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{}`)
	}))
	e := newEnv(t, envOpts{standby: true, tweak: func(o *Options) {
		o.Leader = func(context.Context) (string, error) { return strings.TrimPrefix(leader, "https://"), nil }
		o.PeerTLS = src[pki.Core].ClientTLS(pki.Core)
	}})
	req, _ := http.NewRequest("POST", e.url+"/vigo.core.v1.TVService/GetStatus", bytes.NewReader([]byte("{}")))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: "session-token"})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if seen != "session-token" {
		t.Errorf("leader saw cookie %q", seen)
	}
	if !strings.Contains(strings.Join(resp.Header.Values("Set-Cookie"), ";"), "from_leader=1") {
		t.Errorf("Set-Cookie not passed back: %v", resp.Header)
	}
}
