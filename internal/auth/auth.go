// Package auth protects the browser listener with a single password: no
// username, the first visitor sets it. Sessions live in an HttpOnly cookie
// whose token is stored only as a SHA-256 hash. See docs/design.md
// "Authentication".
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	corev1 "github.com/yuha0/vigo/gen/go/vigo/core/v1"
	"github.com/yuha0/vigo/gen/go/vigo/core/v1/corev1connect"
	"github.com/yuha0/vigo/internal/metrics"
)

const (
	CookieName = "vigo_session"
	sessionTTL = 30 * 24 * time.Hour
	// refreshEvery limits sliding-expiry writes to one per hour per session.
	refreshEvery = time.Hour
	// hashSlots bounds concurrent argon2 work: each hash takes 64 MiB.
	hashSlots = 2
)

type Options struct {
	Pool    *pgxpool.Pool
	Metrics *metrics.Metrics
	Log     *slog.Logger
	// TrustedProxies are the networks whose X-Forwarded-For is believed.
	TrustedProxies []netip.Prefix
	// PublicOrigins are accepted in Origin/Referer besides the request's
	// own host, e.g. "https://vigo.example.com" behind a proxy that
	// rewrites Host.
	PublicOrigins []string
	// Params overrides the argon2id parameters of new hashes; tests only.
	Params *Params
	Now    func() time.Time
}

type Service struct {
	o       Options
	pool    *pgxpool.Pool
	log     *slog.Logger
	params  Params
	limit   *limiter
	slots   chan struct{}
	origins map[string]bool
	logins  *prometheus.CounterVec
}

var _ corev1connect.AuthServiceHandler = (*Service)(nil)

func New(o Options) *Service {
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Metrics == nil {
		o.Metrics = metrics.New()
	}
	s := &Service{
		o:       o,
		pool:    o.Pool,
		log:     o.Log,
		params:  DefaultParams,
		limit:   newLimiter(o.Now),
		slots:   make(chan struct{}, hashSlots),
		origins: map[string]bool{},
		logins:  o.Metrics.AuthLogins,
	}
	if o.Params != nil {
		s.params = *o.Params
	}
	for _, origin := range o.PublicOrigins {
		if n, ok := normalizeOrigin(origin); ok {
			s.origins[n] = true
		}
	}
	o.Metrics.Registry.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "vigo_auth_sessions",
		Help: "Unexpired browser sessions.",
	}, func() float64 {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		var n int64
		if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM web_sessions WHERE expires_at > $1`, s.o.Now()).Scan(&n); err != nil {
			return -1
		}
		return float64(n)
	}))
	return s
}

var (
	errWrongPassword = connect.NewError(connect.CodeUnauthenticated, errors.New("Wrong password."))
	errRateLimited   = connect.NewError(connect.CodeResourceExhausted, errors.New("Too many attempts. Wait a minute and try again."))
	errNoSession     = connect.NewError(connect.CodeUnauthenticated, errors.New("Sign in to continue."))
)

func internal(err error) error {
	return connect.NewError(connect.CodeUnavailable, errors.New("The database is unavailable; try again shortly."))
}

// hash and verify run in a bounded number of slots.
func (s *Service) work(ctx context.Context, fn func()) error {
	select {
	case s.slots <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-s.slots }()
	fn()
	return nil
}

func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// cookie builds the session cookie; an empty token clears it.
func cookie(token string, expires time.Time) *http.Cookie {
	c := &http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	}
	if token == "" {
		c.MaxAge = -1
	} else {
		c.Expires = expires
	}
	return c
}

func tokenFrom(h http.Header) string {
	r := http.Request{Header: h}
	c, err := r.Cookie(CookieName)
	if err != nil {
		return ""
	}
	return c.Value
}

func (s *Service) createSession(ctx context.Context, tx pgx.Tx) (*http.Cookie, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	now := s.o.Now()
	expires := now.Add(sessionTTL)
	var err error
	if tx != nil {
		_, err = tx.Exec(ctx, `INSERT INTO web_sessions (token_hash, created_at, last_used_at, expires_at) VALUES ($1, $2, $2, $3)`, hashToken(token), now, expires)
	} else {
		_, err = s.pool.Exec(ctx, `INSERT INTO web_sessions (token_hash, created_at, last_used_at, expires_at) VALUES ($1, $2, $2, $3)`, hashToken(token), now, expires)
	}
	if err != nil {
		return nil, err
	}
	return cookie(token, expires), nil
}

// Valid reports whether token names an unexpired session, sliding its
// expiry forward at most once per refreshEvery.
func (s *Service) Valid(ctx context.Context, token string) (bool, error) {
	if token == "" {
		return false, nil
	}
	now := s.o.Now()
	var lastUsed time.Time
	err := s.pool.QueryRow(ctx, `SELECT last_used_at FROM web_sessions WHERE token_hash = $1 AND expires_at > $2`,
		hashToken(token), now).Scan(&lastUsed)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if now.Sub(lastUsed) >= refreshEvery {
		if _, err := s.pool.Exec(ctx, `UPDATE web_sessions SET last_used_at = $2, expires_at = $3 WHERE token_hash = $1`,
			hashToken(token), now, now.Add(sessionTTL)); err != nil {
			s.log.Warn("refresh session", "err", err)
		}
	}
	return true, nil
}

func (s *Service) passwordHash(ctx context.Context) (string, bool, error) {
	var h string
	err := s.pool.QueryRow(ctx, `SELECT hash FROM web_password WHERE id = 1`).Scan(&h)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	return h, err == nil, err
}

// clientAddr is the client's address: the connection's, or the last
// untrusted hop of X-Forwarded-For when the connection comes from a trusted
// proxy.
func (s *Service) clientAddr(remote string, h http.Header) string {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	if !s.trusted(addr) {
		return addr.String()
	}
	hops := strings.Split(strings.Join(h.Values("X-Forwarded-For"), ","), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		hop, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			break
		}
		if !s.trusted(hop) {
			return hop.String()
		}
		addr = hop
	}
	return addr.String()
}

func (s *Service) trusted(a netip.Addr) bool {
	a = a.Unmap()
	for _, p := range s.o.TrustedProxies {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

func (s *Service) GetAuthState(ctx context.Context, req *connect.Request[corev1.GetAuthStateRequest]) (*connect.Response[corev1.GetAuthStateResponse], error) {
	_, set, err := s.passwordHash(ctx)
	if err != nil {
		return nil, internal(err)
	}
	ok, err := s.Valid(ctx, tokenFrom(req.Header()))
	if err != nil {
		return nil, internal(err)
	}
	return connect.NewResponse(&corev1.GetAuthStateResponse{
		PasswordSet:       set,
		Authenticated:     set && ok,
		MinPasswordLength: MinPasswordLength,
	}), nil
}

func (s *Service) SetupPassword(ctx context.Context, req *connect.Request[corev1.SetupPasswordRequest]) (*connect.Response[corev1.SetupPasswordResponse], error) {
	if !s.limit.allow(s.clientAddr(req.Peer().Addr, req.Header())) {
		return nil, errRateLimited
	}
	if err := checkPassword(req.Msg.Password); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if _, set, err := s.passwordHash(ctx); err != nil {
		return nil, internal(err)
	} else if set {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("A password is already set; sign in instead."))
	}
	var phc string
	var herr error
	if err := s.work(ctx, func() { phc, herr = hashPassword(req.Msg.Password, s.params) }); err != nil {
		return nil, err
	}
	if herr != nil {
		return nil, internal(herr)
	}
	var c *http.Cookie
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// The single-row insert decides concurrent first visitors: only one
		// insert can succeed.
		tag, err := tx.Exec(ctx, `INSERT INTO web_password (id, hash) VALUES (1, $1) ON CONFLICT (id) DO NOTHING`, phc)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errAlreadySet
		}
		c, err = s.createSession(ctx, tx)
		return err
	})
	if errors.Is(err, errAlreadySet) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("A password is already set; sign in instead."))
	}
	if err != nil {
		return nil, internal(err)
	}
	s.log.Info("web password set")
	resp := connect.NewResponse(&corev1.SetupPasswordResponse{})
	resp.Header().Add("Set-Cookie", c.String())
	return resp, nil
}

var errAlreadySet = errors.New("already set")

// checkCurrent verifies a password against the stored hash. Every failure,
// including no password being set, looks the same to the caller.
func (s *Service) checkCurrent(ctx context.Context, password string) (bool, error) {
	phc, set, err := s.passwordHash(ctx)
	if err != nil {
		return false, internal(err)
	}
	if !set || len(password) > maxPasswordBytes {
		return false, nil
	}
	var ok bool
	if err := s.work(ctx, func() { ok, err = verifyPassword(password, phc) }); err != nil {
		return false, err
	}
	if err != nil {
		s.log.Error("stored password hash is unreadable", "err", err)
		return false, nil
	}
	return ok, nil
}

func (s *Service) Login(ctx context.Context, req *connect.Request[corev1.LoginRequest]) (*connect.Response[corev1.LoginResponse], error) {
	if !s.limit.allow(s.clientAddr(req.Peer().Addr, req.Header())) {
		s.logins.WithLabelValues("rate_limited").Inc()
		return nil, errRateLimited
	}
	ok, err := s.checkCurrent(ctx, req.Msg.Password)
	if err != nil {
		return nil, err
	}
	if !ok {
		s.logins.WithLabelValues("wrong_password").Inc()
		s.log.Warn("failed sign-in", "client", s.clientAddr(req.Peer().Addr, req.Header()))
		return nil, errWrongPassword
	}
	c, err := s.createSession(ctx, nil)
	if err != nil {
		return nil, internal(err)
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM web_sessions WHERE expires_at <= $1`, s.o.Now()); err != nil {
		s.log.Warn("delete expired sessions", "err", err)
	}
	s.logins.WithLabelValues("ok").Inc()
	resp := connect.NewResponse(&corev1.LoginResponse{})
	resp.Header().Add("Set-Cookie", c.String())
	return resp, nil
}

func (s *Service) Logout(ctx context.Context, req *connect.Request[corev1.LogoutRequest]) (*connect.Response[corev1.LogoutResponse], error) {
	token := tokenFrom(req.Header())
	if ok, err := s.Valid(ctx, token); err != nil {
		return nil, internal(err)
	} else if !ok {
		e := connect.NewError(connect.CodeUnauthenticated, errNoSession.Unwrap())
		e.Meta().Add("Set-Cookie", cookie("", time.Time{}).String())
		return nil, e
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM web_sessions WHERE token_hash = $1`, hashToken(token)); err != nil {
		return nil, internal(err)
	}
	resp := connect.NewResponse(&corev1.LogoutResponse{})
	resp.Header().Add("Set-Cookie", cookie("", time.Time{}).String())
	return resp, nil
}

func (s *Service) ChangePassword(ctx context.Context, req *connect.Request[corev1.ChangePasswordRequest]) (*connect.Response[corev1.ChangePasswordResponse], error) {
	token := tokenFrom(req.Header())
	if ok, err := s.Valid(ctx, token); err != nil {
		return nil, internal(err)
	} else if !ok {
		return nil, errNoSession
	}
	if !s.limit.allow(s.clientAddr(req.Peer().Addr, req.Header())) {
		return nil, errRateLimited
	}
	if err := checkPassword(req.Msg.NewPassword); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	ok, err := s.checkCurrent(ctx, req.Msg.CurrentPassword)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("The current password is wrong."))
	}
	var phc string
	var herr error
	if err := s.work(ctx, func() { phc, herr = hashPassword(req.Msg.NewPassword, s.params) }); err != nil {
		return nil, err
	}
	if herr != nil {
		return nil, internal(herr)
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE web_password SET hash = $1, updated_at = now() WHERE id = 1`, phc); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM web_sessions WHERE token_hash <> $1`, hashToken(token))
		return err
	})
	if err != nil {
		return nil, internal(err)
	}
	s.log.Info("web password changed; other sessions ended")
	return connect.NewResponse(&corev1.ChangePasswordResponse{}), nil
}

// Reset removes the password and every session, so the next visitor sets
// a new password.
func Reset(ctx context.Context, pool *pgxpool.Pool) error {
	return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM web_password`); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM web_sessions`)
		return err
	})
}

func normalizeOrigin(o string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(o))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", false
	}
	return strings.ToLower(u.Scheme + "://" + u.Host), true
}

// sameOrigin requires a state-changing request's Origin, or failing that
// its Referer, to name this host or a configured public origin.
func (s *Service) sameOrigin(r *http.Request) bool {
	src := r.Header.Get("Origin")
	if src == "" || src == "null" {
		src = r.Header.Get("Referer")
	}
	n, ok := normalizeOrigin(src)
	if !ok {
		return false
	}
	if s.origins[n] {
		return true
	}
	_, host, _ := strings.Cut(n, "://")
	return strings.EqualFold(host, r.Host)
}

func writeJSON(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}{code, msg})
}

func stateChanging(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	return true
}

// CheckOrigin rejects cross-origin state-changing requests with 403. The
// SameSite=Strict cookie is the primary defence; this is defence in depth.
func (s *Service) CheckOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if stateChanging(r.Method) && !s.sameOrigin(r) {
			writeJSON(w, http.StatusForbidden, "permission_denied", "The request did not come from this site.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Require answers 401 unauthenticated without a valid session, and checks
// the origin of state-changing requests.
func (s *Service) Require(next http.Handler) http.Handler {
	return s.CheckOrigin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ok, err := s.Valid(r.Context(), tokenFrom(r.Header))
		if err != nil {
			s.log.Error("check session", "err", err)
			writeJSON(w, http.StatusServiceUnavailable, "unavailable", "The database is unavailable; try again shortly.")
			return
		}
		if !ok {
			writeJSON(w, http.StatusUnauthorized, "unauthenticated", "Sign in to continue.")
			return
		}
		next.ServeHTTP(w, r)
	}))
}
