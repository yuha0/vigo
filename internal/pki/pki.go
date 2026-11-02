// Package pki loads the mutual-TLS material in --pki-dir (tls.crt, tls.key,
// ca.crt: the layout of a cert-manager Certificate Secret), reloads it when
// it changes, and authorizes peers by URI SAN rather than hostname.
package pki

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
)

// Core is vigo-core's identity.
const Core = "spiffe://vigo/core"

const integrationPrefix = "spiffe://vigo/integration/"

var kindPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// IntegrationIdentity is the URI SAN of integration kind.
func IntegrationIdentity(kind string) string { return integrationPrefix + kind }

// IntegrationKind returns the kind of an integration identity.
func IntegrationKind(id string) (string, bool) {
	kind, ok := strings.CutPrefix(id, integrationPrefix)
	if !ok || !kindPattern.MatchString(kind) {
		return "", false
	}
	return kind, true
}

// Identity returns the certificate's single spiffe:// URI SAN.
func Identity(cert *x509.Certificate) (string, error) {
	var ids []string
	for _, u := range cert.URIs {
		if u.Scheme == "spiffe" {
			ids = append(ids, u.String())
		}
	}
	if len(ids) != 1 {
		return "", fmt.Errorf("pki: certificate has %d spiffe URI SANs, want 1", len(ids))
	}
	return ids[0], nil
}

// PeerIdentity is the identity of a request's verified client certificate.
func PeerIdentity(r *http.Request) (string, bool) {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return "", false
	}
	id, err := Identity(r.TLS.PeerCertificates[0])
	return id, err == nil
}

type material struct {
	cert *tls.Certificate
	leaf *x509.Certificate
	pool *x509.CertPool
	sum  [32]byte
}

// Source holds the current material. Every handshake reads it afresh, so a
// reload takes effect without restarting listeners or clients.
type Source struct {
	dir    string
	log    *slog.Logger
	onLoad func(leaf *x509.Certificate)
	cur    atomic.Pointer[material]
}

type Option func(*Source)

// OnLoad is called with the leaf certificate after every successful load.
func OnLoad(fn func(leaf *x509.Certificate)) Option { return func(s *Source) { s.onLoad = fn } }

func WithLogger(l *slog.Logger) Option { return func(s *Source) { s.log = l } }

// Load reads dir once. Call Watch to keep it current.
func Load(dir string, opts ...Option) (*Source, error) {
	s := &Source{dir: dir, log: slog.New(slog.DiscardHandler)}
	for _, o := range opts {
		o(s)
	}
	if _, err := s.Reload(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Source) read() (*material, error) {
	var files [3][]byte
	for i, name := range []string{"tls.crt", "tls.key", "ca.crt"} {
		b, err := os.ReadFile(filepath.Join(s.dir, name))
		if err != nil {
			return nil, fmt.Errorf("pki: %w", err)
		}
		files[i] = b
	}
	m := &material{sum: sha256.Sum256(bytes.Join(files[:], []byte{0}))}
	cert, err := tls.X509KeyPair(files[0], files[1])
	if err != nil {
		return nil, fmt.Errorf("pki: %s: %w", s.dir, err)
	}
	m.cert = &cert
	m.leaf = cert.Leaf
	if _, err := Identity(m.leaf); err != nil {
		return nil, err
	}
	m.pool = x509.NewCertPool()
	if !m.pool.AppendCertsFromPEM(files[2]) {
		return nil, fmt.Errorf("pki: %s/ca.crt has no certificates", s.dir)
	}
	return m, nil
}

// Reload rereads the directory and reports whether the material changed. On
// error the previous material stays in use.
func (s *Source) Reload() (bool, error) {
	m, err := s.read()
	if err != nil {
		return false, err
	}
	if old := s.cur.Load(); old != nil && old.sum == m.sum {
		return false, nil
	}
	s.cur.Store(m)
	if s.onLoad != nil {
		s.onLoad(m.leaf)
	}
	return true, nil
}

// Watch polls for changes until ctx ends. Polling the content, rather than
// watching inodes, follows the symlink swaps of Kubernetes Secret volumes
// and tolerates reading mid-swap: a mismatched pair fails to load and is
// retried on the next tick.
func (s *Source) Watch(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			changed, err := s.Reload()
			switch {
			case err != nil:
				s.log.Warn("reload TLS material", "dir", s.dir, "err", err)
			case changed:
				leaf := s.Leaf()
				s.log.Info("reloaded TLS material", "dir", s.dir, "serial", leaf.SerialNumber.String(), "not_after", leaf.NotAfter)
			}
		}
	}
}

// Leaf is the current certificate.
func (s *Source) Leaf() *x509.Certificate { return s.cur.Load().leaf }

// verify checks a peer chain against the current CA and returns the peer's
// identity.
func (s *Source) verify(certs []*x509.Certificate, usage x509.ExtKeyUsage) (string, error) {
	if len(certs) == 0 {
		return "", errors.New("pki: peer sent no certificate")
	}
	inter := x509.NewCertPool()
	for _, c := range certs[1:] {
		inter.AddCert(c)
	}
	_, err := certs[0].Verify(x509.VerifyOptions{
		Roots:         s.cur.Load().pool,
		Intermediates: inter,
		KeyUsages:     []x509.ExtKeyUsage{usage},
	})
	if err != nil {
		return "", fmt.Errorf("pki: %w", err)
	}
	return Identity(certs[0])
}

// ServerTLS requires a client certificate chaining to the CA whose identity
// allow accepts.
func (s *Source) ServerTLS(allow func(id string) bool) *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{"h2", "http/1.1"},
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			return s.cur.Load().cert, nil
		},
		ClientAuth: tls.RequireAnyClientCert,
		VerifyConnection: func(cs tls.ConnectionState) error {
			id, err := s.verify(cs.PeerCertificates, x509.ExtKeyUsageClientAuth)
			if err != nil {
				return err
			}
			if !allow(id) {
				return fmt.Errorf("pki: identity %s is not allowed", id)
			}
			return nil
		},
	}
}

// ClientTLS presents this identity and accepts only a server chaining to
// the CA with identity want. Host names are not checked: peers are
// authorized by URI SAN.
func (s *Source) ClientTLS(want string) *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			return s.cur.Load().cert, nil
		},
		// Chain and identity are verified in VerifyConnection.
		InsecureSkipVerify: true,
		VerifyConnection: func(cs tls.ConnectionState) error {
			id, err := s.verify(cs.PeerCertificates, x509.ExtKeyUsageServerAuth)
			if err != nil {
				return err
			}
			if id != want {
				return fmt.Errorf("pki: server identity is %s, want %s", id, want)
			}
			return nil
		},
	}
}

type peerKey struct{}

// WithPeer returns ctx carrying the authenticated peer identity.
func WithPeer(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, peerKey{}, id)
}

// Peer returns the identity stored by WithPeer or Authenticate.
func Peer(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(peerKey{}).(string)
	return id, ok
}

// Authenticate stores each request's verified client identity in its
// context and refuses requests without one. Use it behind ServerTLS.
func Authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := PeerIdentity(r)
		if !ok {
			http.Error(w, "client certificate required", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(WithPeer(r.Context(), id)))
	})
}
