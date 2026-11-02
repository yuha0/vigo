// Package coreapi is vigo-core's HTTP surface: the Connect services, the
// thumbnail and upload endpoints, forwarding to the leader, the reverse
// proxy to vigo-home, health and metrics, and the embedded web UI.
package coreapi

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"

	"github.com/yuha0/vigo/gen/go/vigo/core/v1/corev1connect"
	"github.com/yuha0/vigo/gen/go/vigo/home/v1/homev1connect"
	"github.com/yuha0/vigo/gen/go/vigo/integration/v1/integrationv1connect"
	"github.com/yuha0/vigo/internal/library"
	"github.com/yuha0/vigo/internal/metrics"
	"github.com/yuha0/vigo/internal/pki"
	"github.com/yuha0/vigo/internal/tv"
)

const defaultMaxUpload = 64 << 20

// forwardedHeader marks a request a standby replica forwarded to the
// leader, so a replica that is no longer leader refuses it rather than
// forwarding again.
const forwardedHeader = "Vigo-Forwarded-By"

type Options struct {
	TV      *tv.Service
	Metrics *metrics.Metrics
	Log     *slog.Logger
	// Web is the UI to serve; nil uses the embedded build.
	Web fs.FS
	// Ready reports readiness; nil means always ready.
	Ready func() bool
	// MaxUploadBytes bounds a POST /sources/{id}/files body.
	MaxUploadBytes int64
	// Library serves LibraryService; nil answers unavailable.
	Library *library.Library

	// Leader resolves the leader's mTLS address (host:port). A replica that
	// is not leader forwards API requests there with PeerTLS. Nil disables
	// forwarding.
	Leader  func(ctx context.Context) (string, error)
	PeerTLS *tls.Config

	// HomeEndpoint resolves the base URL registered by the homekit
	// integration's leader; HomeTLS is the client configuration for it.
	HomeEndpoint func(ctx context.Context) (string, error)
	HomeTLS      *tls.Config

	// Integration is served on the internal listener only.
	Integration integrationv1connect.IntegrationServiceHandler
}

// Server builds the handlers of vigo-core's three listeners.
type Server struct {
	opts   Options
	api    http.Handler
	leader *cache
	home   *cache
}

func New(opts Options) (*Server, error) {
	if opts.Log == nil {
		opts.Log = slog.New(slog.DiscardHandler)
	}
	if opts.MaxUploadBytes <= 0 {
		opts.MaxUploadBytes = defaultMaxUpload
	}
	if opts.Web == nil {
		opts.Web = embeddedWeb()
	}
	if opts.Leader != nil && opts.PeerTLS == nil {
		return nil, errors.New("coreapi: Leader needs PeerTLS")
	}
	s := &Server{opts: opts}
	if opts.Leader != nil {
		s.leader = newCache(2*time.Second, opts.Leader)
	}
	if opts.HomeEndpoint != nil {
		s.home = newCache(5*time.Second, opts.HomeEndpoint)
	}

	h := &handlers{tv: opts.TV}
	api := http.NewServeMux()
	api.Handle(corev1connect.NewTVServiceHandler(h))
	api.Handle(corev1connect.NewArtServiceHandler(h))
	api.Handle(corev1connect.NewPairingServiceHandler(h))
	api.Handle(corev1connect.NewConfigServiceHandler(h))
	api.HandleFunc("GET /art/{content_id}/thumbnail", thumbnailHandler(opts.TV))
	lh := &libraryHandlers{lib: opts.Library}
	api.Handle(corev1connect.NewLibraryServiceHandler(lh))
	api.HandleFunc("GET /library/{artwork_id}/thumbnail", imageHandler(opts.Library, (*library.Library).Thumbnail))
	api.HandleFunc("GET /library/{artwork_id}/image", imageHandler(opts.Library, (*library.Library).Preview))
	api.HandleFunc("POST /sources/{source_id}/files", sourceFileHandler(opts.Library, opts.MaxUploadBytes, opts.Log))
	s.api = s.toLeader(api)
	return s, nil
}

func isAPI(path string) bool {
	for _, p := range []string{"/vigo.core.v1.", "/art/", "/library/", "/sources/"} {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

// Public is the browser listener (:8080): the API, the HomeService proxy
// and the web UI.
func (s *Server) Public() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/"+homev1connect.HomeServiceName+"/", s.homeProxy())
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isAPI(r.URL.Path) {
			r.Header.Del(forwardedHeader)
			s.api.ServeHTTP(w, r)
			return
		}
		webHandler(s.opts.Web).ServeHTTP(w, r)
	}))
	return mux
}

// AllowInternal accepts the identities that may call the internal listener.
func AllowInternal(id string) bool {
	_, ok := pki.IntegrationKind(id)
	return ok || id == pki.Core
}

// Internal is the mTLS listener (:8443): the API for integrations and for
// standby replicas forwarding to the leader, and IntegrationService. Wrap
// it in pki.Authenticate.
func (s *Server) Internal() http.Handler {
	mux := http.NewServeMux()
	if s.opts.Integration != nil {
		mux.Handle(integrationv1connect.NewIntegrationServiceHandler(s.opts.Integration))
	}
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isAPI(r.URL.Path) {
			http.NotFound(w, r)
			return
		}
		if id, _ := pki.Peer(r.Context()); id != pki.Core {
			r.Header.Del(forwardedHeader)
		}
		s.api.ServeHTTP(w, r)
	}))
	return mux
}

// Ops is the plain listener (:9090) for probes and Prometheus.
func (s *Server) Ops() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, "ok\n")
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		if s.opts.Ready != nil && !s.opts.Ready() {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		io.WriteString(w, "ok\n")
	})
	if s.opts.Metrics != nil {
		mux.Handle("GET /metrics", s.opts.Metrics.Handler())
	}
	return mux
}

// NewServer returns a plain-HTTP server speaking HTTP/1.1 and unencrypted
// HTTP/2. It sets no write timeout because WatchEvents streams
// indefinitely.
func NewServer(addr string, handler http.Handler) *http.Server {
	var protocols http.Protocols
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		Protocols:         &protocols,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
}

// NewTLSServer returns an HTTPS server speaking HTTP/1.1 and HTTP/2. Serve
// it with ServeTLS(ln, "", "").
func NewTLSServer(addr string, handler http.Handler, cfg *tls.Config) *http.Server {
	srv := NewServer(addr, handler)
	var protocols http.Protocols
	protocols.SetHTTP1(true)
	protocols.SetHTTP2(true)
	srv.Protocols = &protocols
	srv.TLSConfig = cfg
	return srv
}

type targetKey struct{}

// toLeader serves locally on the leader and forwards elsewhere.
func (s *Server) toLeader(local http.Handler) http.Handler {
	if s.leader == nil {
		return local
	}
	var protocols http.Protocols
	protocols.SetHTTP2(true)
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(pr.In.Context().Value(targetKey{}).(*url.URL))
			pr.SetXForwarded()
			pr.Out.Header.Set(forwardedHeader, s.opts.TV.Status().GetReplica())
		},
		Transport:     &http.Transport{TLSClientConfig: s.opts.PeerTLS, Protocols: &protocols},
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			s.opts.Log.Warn("forward to leader", "path", r.URL.Path, "err", err)
			s.leader.invalidate()
			writeError(w, connect.CodeUnavailable, "the leader replica is unavailable")
		},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.opts.TV.IsLeader() {
			local.ServeHTTP(w, r)
			return
		}
		if r.Header.Get(forwardedHeader) != "" {
			writeError(w, connect.CodeUnavailable, "this replica is not the leader")
			return
		}
		addr, err := s.leader.get(r.Context())
		if err != nil {
			writeError(w, connect.CodeUnavailable, "no leader is elected")
			return
		}
		target := &url.URL{Scheme: "https", Host: addr}
		proxy.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), targetKey{}, target)))
	})
}

// homeProxy forwards the HomeService to the homekit integration's
// registered endpoint over mTLS. vigo-home serves HTTP/1.1 only.
func (s *Server) homeProxy() http.Handler {
	if s.home == nil || s.opts.HomeTLS == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeError(w, connect.CodeUnavailable, "vigo-home is not configured")
		})
	}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(pr.In.Context().Value(targetKey{}).(*url.URL))
			pr.SetXForwarded()
		},
		Transport:     &http.Transport{TLSClientConfig: s.opts.HomeTLS, ResponseHeaderTimeout: 30 * time.Second},
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			s.opts.Log.Warn("vigo-home proxy", "path", r.URL.Path, "err", err)
			s.home.invalidate()
			writeError(w, connect.CodeUnavailable, "vigo-home is unavailable")
		},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ep, err := s.home.get(r.Context())
		if err != nil {
			writeError(w, connect.CodeUnavailable, "no vigo-home leader is registered")
			return
		}
		target, err := url.Parse(ep)
		if err != nil {
			writeError(w, connect.CodeUnavailable, "vigo-home registered an invalid endpoint")
			return
		}
		proxy.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), targetKey{}, target)))
	})
}

// cache memoizes a lookup briefly; failures are not cached.
type cache struct {
	ttl    time.Duration
	lookup func(context.Context) (string, error)

	mu    sync.Mutex
	value string
	at    time.Time
}

func newCache(ttl time.Duration, lookup func(context.Context) (string, error)) *cache {
	return &cache{ttl: ttl, lookup: lookup}
}

func (c *cache) get(ctx context.Context) (string, error) {
	c.mu.Lock()
	if c.value != "" && time.Since(c.at) < c.ttl {
		v := c.value
		c.mu.Unlock()
		return v, nil
	}
	c.mu.Unlock()
	v, err := c.lookup(ctx)
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	c.value, c.at = v, time.Now()
	c.mu.Unlock()
	return v, nil
}

func (c *cache) invalidate() {
	c.mu.Lock()
	c.value = ""
	c.mu.Unlock()
}

func thumbnailHandler(svc *tv.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data, etag, err := svc.Thumbnail(r.Context(), r.PathValue("content_id"))
		if err != nil {
			writeErr(w, err)
			return
		}
		// Content ids may be reused after a delete, so revalidate every time;
		// the ETag makes that a 304.
		w.Header().Set("Cache-Control", "private, no-cache")
		serveJPEG(w, r, data, etag)
	}
}

func serveJPEG(w http.ResponseWriter, r *http.Request, data []byte, etag string) {
	w.Header().Set("ETag", etag)
	if match := r.Header.Get("If-None-Match"); match != "" && etagMatches(match, etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Content-Length", fmt.Sprint(len(data)))
	w.Write(data)
}

func etagMatches(header, etag string) bool {
	for _, t := range strings.Split(header, ",") {
		t = strings.TrimPrefix(strings.TrimSpace(t), "W/")
		if t == etag || t == "*" {
			return true
		}
	}
	return false
}
