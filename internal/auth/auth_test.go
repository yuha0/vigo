package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/yuha0/vigo/gen/go/vigo/core/v1/corev1connect"
	"github.com/yuha0/vigo/internal/db/dbtest"
	"github.com/yuha0/vigo/internal/metrics"
)

var testParams = Params{MemoryKiB: 1024, Time: 1, Threads: 1, SaltLen: 16, KeyLen: 32}

func TestPasswordHash(t *testing.T) {
	phc, err := hashPassword("correct horse battery", testParams)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(phc, "$argon2id$v=19$m=1024,t=1,p=1$") {
		t.Errorf("phc = %s", phc)
	}
	other, _ := hashPassword("correct horse battery", testParams)
	if other == phc {
		t.Error("salt reused")
	}
	for pw, want := range map[string]bool{"correct horse battery": true, "correct horse batterY": false, "": false} {
		if ok, err := verifyPassword(pw, phc); err != nil || ok != want {
			t.Errorf("verify(%q) = %v, %v", pw, ok, err)
		}
	}
	for _, bad := range []string{"", "$argon2i$v=19$m=1,t=1,p=1$AA$AA", "$argon2id$v=19$m=x$AA$AA", "$argon2id$v=19$m=1024,t=1,p=1$!!$AA"} {
		if _, err := verifyPassword("x", bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	defaults, _ := hashPassword("x", DefaultParams)
	if !strings.HasPrefix(defaults, "$argon2id$v=19$m=65536,t=3,p=4$") {
		t.Errorf("default params = %s", defaults)
	}
}

func TestPasswordRule(t *testing.T) {
	for pw, ok := range map[string]bool{
		"twelve chars":                  true,
		"short":                         false,
		"ünïcödé-půtz":                  true,
		strings.Repeat("a", 1024):       true,
		strings.Repeat("a", 1025):       false,
		strings.Repeat("é", 600):        false,
		"invalid utf8 \xff\xfe\xfd pad": false,
	} {
		if err := checkPassword(pw); (err == nil) != ok {
			t.Errorf("checkPassword(%.20q) = %v", pw, err)
		}
	}
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func TestLimiter(t *testing.T) {
	c := &clock{t: time.Unix(1_000_000, 0)}
	l := newLimiter(c.now)
	for i := range 5 {
		if !l.allow("a") {
			t.Fatalf("attempt %d refused", i+1)
		}
	}
	if l.allow("a") {
		t.Error("sixth attempt allowed")
	}
	if !l.allow("b") {
		t.Error("another client limited")
	}
	c.advance(10 * time.Second)
	if !l.allow("a") || l.allow("a") {
		t.Error("expected exactly one attempt after 10s")
	}
	for i := range 100 {
		l.allow(string(rune('c' + i)))
	}
	if l.allow("fresh") {
		t.Error("global cap not applied")
	}
}

func TestClientAddr(t *testing.T) {
	s := &Service{o: Options{TrustedProxies: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}}}
	h := func(xff ...string) http.Header {
		hd := http.Header{}
		for _, v := range xff {
			hd.Add("X-Forwarded-For", v)
		}
		return hd
	}
	tests := []struct {
		remote string
		h      http.Header
		want   string
	}{
		{"192.0.2.7:5000", h("198.51.100.1"), "192.0.2.7"},
		{"10.1.2.3:5000", h("198.51.100.1"), "198.51.100.1"},
		{"10.1.2.3:5000", h("203.0.113.9, 198.51.100.1, 10.9.9.9"), "198.51.100.1"},
		{"10.1.2.3:5000", h("203.0.113.9", "198.51.100.1"), "198.51.100.1"},
		{"10.1.2.3:5000", h(), "10.1.2.3"},
		{"10.1.2.3:5000", h("garbage"), "10.1.2.3"},
		{"[::ffff:10.1.2.3]:5000", h("198.51.100.1"), "198.51.100.1"},
	}
	for _, tt := range tests {
		if got := s.clientAddr(tt.remote, tt.h); got != tt.want {
			t.Errorf("clientAddr(%s, %v) = %s, want %s", tt.remote, tt.h, got, tt.want)
		}
	}
}

func TestSameOrigin(t *testing.T) {
	s := New(Options{PublicOrigins: []string{"https://vigo.example.com"}})
	tests := []struct {
		origin, referer string
		ok              bool
	}{
		{"http://core.local:8080", "", true},
		{"https://core.local:8080", "", true},
		{"https://vigo.example.com", "", true},
		{"https://evil.example", "", false},
		{"", "http://core.local:8080/gallery", true},
		{"", "https://evil.example/x", false},
		{"null", "", false},
		{"", "", false},
	}
	for _, tt := range tests {
		r := httptest.NewRequest("POST", "http://core.local:8080/x", nil)
		if tt.origin != "" {
			r.Header.Set("Origin", tt.origin)
		}
		if tt.referer != "" {
			r.Header.Set("Referer", tt.referer)
		}
		if got := s.sameOrigin(r); got != tt.ok {
			t.Errorf("origin %q referer %q: %v", tt.origin, tt.referer, got)
		}
	}
}

// env serves AuthService and one protected endpoint, as the public
// listener does.
type env struct {
	t     *testing.T
	s     *Service
	url   string
	clock *clock
	m     *metrics.Metrics
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := dbtest.Open(t)
	c := &clock{t: time.Now().UTC().Truncate(time.Second)}
	m := metrics.New()
	s := New(Options{
		Pool:           e.DB.Pool(),
		Metrics:        m,
		Params:         &testParams,
		Now:            c.now,
		TrustedProxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")},
	})
	mux := http.NewServeMux()
	path, h := corev1connect.NewAuthServiceHandler(s)
	mux.Handle(path, s.CheckOrigin(h))
	mux.Handle("/protected", s.Require(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, "secret")
	})))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &env{t: t, s: s, url: srv.URL, clock: c, m: m}
}

type reply struct {
	status int
	body   map[string]any
	cookie *http.Cookie
}

// rpc calls an AuthService method with the Connect JSON protocol from a
// same-origin page. client sets X-Forwarded-For.
func (e *env) rpc(method, token, client string, msg any) reply {
	e.t.Helper()
	b, _ := json.Marshal(msg)
	req, _ := http.NewRequest("POST", e.url+"/vigo.core.v1.AuthService/"+method, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", e.url)
	if token != "" {
		req.AddCookie(&http.Cookie{Name: CookieName, Value: token})
	}
	if client != "" {
		req.Header.Set("X-Forwarded-For", client)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	r := reply{status: resp.StatusCode}
	json.NewDecoder(resp.Body).Decode(&r.body)
	for _, c := range resp.Cookies() {
		if c.Name == CookieName {
			r.cookie = c
		}
	}
	return r
}

func (e *env) protected(token string) int {
	e.t.Helper()
	req, _ := http.NewRequest("GET", e.url+"/protected", nil)
	if token != "" {
		req.AddCookie(&http.Cookie{Name: CookieName, Value: token})
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

const password = "correct horse battery"

func (e *env) setup() string {
	e.t.Helper()
	r := e.rpc("SetupPassword", "", "", map[string]string{"password": password})
	if r.status != 200 || r.cookie == nil {
		e.t.Fatalf("setup: %d %v", r.status, r.body)
	}
	return r.cookie.Value
}

func TestSetupOnce(t *testing.T) {
	e := newEnv(t)
	if r := e.rpc("GetAuthState", "", "", map[string]any{}); r.body["passwordSet"] == true || r.body["minPasswordLength"] != float64(12) {
		t.Fatalf("state = %v", r.body)
	}
	if r := e.rpc("SetupPassword", "", "", map[string]string{"password": "too short"}); r.body["code"] != "invalid_argument" {
		t.Errorf("short password: %v", r.body)
	}

	// Concurrent first visitors, from different addresses so the rate
	// limit is not what stops them: exactly one wins.
	var wg sync.WaitGroup
	results := make([]reply, 8)
	for i := range results {
		wg.Go(func() {
			results[i] = e.rpc("SetupPassword", "", "198.51.100."+string(rune('1'+i)), map[string]string{"password": password + string(rune('a'+i))})
		})
	}
	wg.Wait()
	wins := 0
	for _, r := range results {
		switch {
		case r.status == 200 && r.cookie != nil:
			wins++
		case r.body["code"] != "failed_precondition":
			t.Errorf("loser got %d %v", r.status, r.body)
		}
	}
	if wins != 1 {
		t.Fatalf("%d setups succeeded, want 1", wins)
	}
	if r := e.rpc("SetupPassword", "", "", map[string]string{"password": "another long password"}); r.body["code"] != "failed_precondition" {
		t.Errorf("setup after the fact: %v", r.body)
	}
	if r := e.rpc("GetAuthState", "", "", map[string]any{}); r.body["passwordSet"] != true || r.body["authenticated"] == true {
		t.Errorf("state = %v", r.body)
	}
}

func TestLoginLogoutAndCookie(t *testing.T) {
	e := newEnv(t)
	setupToken := e.setup()
	if e.protected("") != 401 || e.protected("forged") != 401 {
		t.Error("protected endpoint open without a session")
	}
	if e.protected(setupToken) != 200 {
		t.Error("setup did not start a session")
	}

	if r := e.rpc("Login", "", "", map[string]string{"password": "wrong password!!"}); r.status != 401 || r.body["code"] != "unauthenticated" || r.cookie != nil {
		t.Errorf("wrong password: %d %v", r.status, r.body)
	}
	r := e.rpc("Login", "", "", map[string]string{"password": password})
	if r.status != 200 || r.cookie == nil {
		t.Fatalf("login: %d %v", r.status, r.body)
	}
	c := r.cookie
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || len(c.Value) < 40 {
		t.Errorf("cookie attributes: %+v", c)
	}
	if d := c.Expires.Sub(e.clock.now()); d < 29*24*time.Hour || d > 31*24*time.Hour {
		t.Errorf("cookie expires in %v", d)
	}
	if st := e.rpc("GetAuthState", c.Value, "", map[string]any{}); st.body["authenticated"] != true {
		t.Errorf("state = %v", st.body)
	}
	var stored []byte
	e.s.pool.QueryRow(context.Background(), `SELECT token_hash FROM web_sessions LIMIT 1`).Scan(&stored)
	if bytes.Contains(stored, []byte(c.Value)) {
		t.Error("token stored in plaintext")
	}

	out := e.rpc("Logout", c.Value, "", map[string]any{})
	if out.status != 200 || out.cookie == nil || out.cookie.MaxAge >= 0 {
		t.Errorf("logout: %d %+v", out.status, out.cookie)
	}
	if e.protected(c.Value) != 401 {
		t.Error("session valid after logout")
	}
	if e.protected(setupToken) != 200 {
		t.Error("logout ended another session")
	}
	if got := testutil.ToFloat64(e.m.AuthLogins.WithLabelValues("ok")); got != 1 {
		t.Errorf("ok logins = %v", got)
	}
	if got := testutil.ToFloat64(e.m.AuthLogins.WithLabelValues("wrong_password")); got != 1 {
		t.Errorf("wrong_password logins = %v", got)
	}
}

func TestLoginRateLimit(t *testing.T) {
	e := newEnv(t)
	e.setup()
	for i := range 5 {
		if r := e.rpc("Login", "", "198.51.100.1", map[string]string{"password": "wrong password!!"}); r.body["code"] != "unauthenticated" {
			t.Fatalf("attempt %d: %v", i+1, r.body)
		}
	}
	for range 2 {
		r := e.rpc("Login", "", "198.51.100.1", map[string]string{"password": password})
		if r.status != 429 || r.body["code"] != "resource_exhausted" {
			t.Errorf("limited: %d %v", r.status, r.body)
		}
	}
	if r := e.rpc("Login", "", "198.51.100.2", map[string]string{"password": password}); r.status != 200 {
		t.Errorf("another client: %d %v", r.status, r.body)
	}
	e.clock.advance(10 * time.Second)
	if r := e.rpc("Login", "", "198.51.100.1", map[string]string{"password": password}); r.status != 200 {
		t.Errorf("after waiting: %d %v", r.status, r.body)
	}
	if got := testutil.ToFloat64(e.m.AuthLogins.WithLabelValues("rate_limited")); got != 2 {
		t.Errorf("rate_limited = %v", got)
	}
}

func TestOriginCheck(t *testing.T) {
	e := newEnv(t)
	b := strings.NewReader(`{"password":"correct horse battery"}`)
	for name, set := range map[string]func(*http.Request){
		"no origin":    func(*http.Request) {},
		"cross origin": func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") },
		"cross referer": func(r *http.Request) {
			r.Header.Set("Referer", "https://evil.example/page")
		},
	} {
		req, _ := http.NewRequest("POST", e.url+"/vigo.core.v1.AuthService/SetupPassword", b)
		req.Header.Set("Content-Type", "application/json")
		set(req)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var body map[string]string
		json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()
		if resp.StatusCode != 403 || body["code"] != "permission_denied" {
			t.Errorf("%s: %d %v", name, resp.StatusCode, body)
		}
	}
	if r := e.rpc("GetAuthState", "", "", map[string]any{}); r.body["passwordSet"] == true {
		t.Error("a cross-origin request set the password")
	}
}

func TestChangePasswordRevokesOtherSessions(t *testing.T) {
	e := newEnv(t)
	a := e.setup()
	b := e.rpc("Login", "", "", map[string]string{"password": password}).cookie.Value

	if r := e.rpc("ChangePassword", "", "", map[string]string{"currentPassword": password, "newPassword": "a brand new password"}); r.body["code"] != "unauthenticated" {
		t.Errorf("without a session: %v", r.body)
	}
	if r := e.rpc("ChangePassword", a, "", map[string]string{"currentPassword": "not the password", "newPassword": "a brand new password"}); r.body["code"] != "permission_denied" {
		t.Errorf("wrong current password: %v", r.body)
	}
	if r := e.rpc("ChangePassword", a, "", map[string]string{"currentPassword": password, "newPassword": "short"}); r.body["code"] != "invalid_argument" {
		t.Errorf("short new password: %v", r.body)
	}
	if r := e.rpc("ChangePassword", a, "", map[string]string{"currentPassword": password, "newPassword": "a brand new password"}); r.status != 200 {
		t.Fatalf("change: %d %v", r.status, r.body)
	}
	if e.protected(a) != 200 {
		t.Error("the changing session ended")
	}
	if e.protected(b) != 401 {
		t.Error("another session survived the change")
	}
	if r := e.rpc("Login", "", "198.51.100.9", map[string]string{"password": password}); r.status != 401 {
		t.Error("old password still works")
	}
	if r := e.rpc("Login", "", "198.51.100.9", map[string]string{"password": "a brand new password"}); r.status != 200 {
		t.Error("new password rejected")
	}
}

func TestSessionExpiry(t *testing.T) {
	e := newEnv(t)
	token := e.setup()
	lastUsed := func() time.Time {
		var t0 time.Time
		e.s.pool.QueryRow(context.Background(), `SELECT last_used_at FROM web_sessions WHERE token_hash = $1`, hashToken(token)).Scan(&t0)
		return t0
	}
	start := lastUsed()

	e.clock.advance(30 * time.Minute)
	e.protected(token)
	if !lastUsed().Equal(start) {
		t.Error("refreshed within the hour")
	}
	e.clock.advance(29 * 24 * time.Hour)
	if e.protected(token) != 200 {
		t.Fatal("expired before 30 days")
	}
	if !lastUsed().After(start) {
		t.Error("use after an hour did not slide the expiry")
	}
	e.clock.advance(29 * 24 * time.Hour)
	if e.protected(token) != 200 {
		t.Error("sliding expiry did not extend the session")
	}
	e.clock.advance(31 * 24 * time.Hour)
	if e.protected(token) != 401 {
		t.Error("idle session did not expire")
	}
}

func TestReset(t *testing.T) {
	e := newEnv(t)
	token := e.setup()
	if err := Reset(context.Background(), e.s.pool); err != nil {
		t.Fatal(err)
	}
	if e.protected(token) != 401 {
		t.Error("session survived the reset")
	}
	if r := e.rpc("GetAuthState", "", "", map[string]any{}); r.body["passwordSet"] == true {
		t.Error("password survived the reset")
	}
	e.setup()
}
