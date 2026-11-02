// Package db is vigo-core's PostgreSQL access: the connection pool,
// embedded migrations, advisory locks and queries. Values that grant access
// are encrypted with internal/secretbox before they are written.
package db

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yuha0/vigo/internal/secretbox"
)

// DefaultLockClass namespaces vigo's advisory locks. Keys are pairs of
// int4: (class, 0) migrations, (class, 1) the vigo-core leader, and
// (class+1, hash of kind) integration leadership.
const DefaultLockClass int32 = 0x76696730

var ErrNotFound = errors.New("db: not found")

type Options struct {
	// URL is a libpq connection URI.
	URL string
	// CAFile, when set, enforces verify-full TLS against this CA bundle,
	// whatever the URL's sslmode says.
	CAFile  string
	Keyring *secretbox.Keyring
	// LockClass overrides DefaultLockClass; tests use it to stay isolated.
	LockClass int32
	// Schema, when set, becomes the search_path; tests use it.
	Schema   string
	MaxConns int32
}

type DB struct {
	pool      *pgxpool.Pool
	connCfg   *pgx.ConnConfig
	keys      *secretbox.Keyring
	lockClass int32
}

// ReadURLFile reads a connection URI from a file, as mounted from a
// CloudNativePG app Secret's uri key.
func ReadURLFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("db: %w", err)
	}
	u := strings.TrimSpace(string(b))
	if u == "" {
		return "", fmt.Errorf("db: %s is empty", path)
	}
	return u, nil
}

// Open connects and pings the database.
func Open(ctx context.Context, o Options) (*DB, error) {
	if o.Keyring == nil {
		return nil, errors.New("db: a keyring is required")
	}
	cfg, err := pgxpool.ParseConfig(o.URL)
	if err != nil {
		// The parse error can echo the URL, password included.
		return nil, errors.New("db: invalid connection URI")
	}
	if o.CAFile != "" {
		pem, err := os.ReadFile(o.CAFile)
		if err != nil {
			return nil, fmt.Errorf("db: %w", err)
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("db: %s has no certificates", o.CAFile)
		}
		cfg.ConnConfig.TLSConfig = &tls.Config{
			MinVersion: tls.VersionTLS12,
			RootCAs:    roots,
			ServerName: cfg.ConnConfig.Host,
		}
		cfg.ConnConfig.Fallbacks = nil
	}
	if o.Schema != "" {
		cfg.ConnConfig.RuntimeParams["search_path"] = o.Schema
	}
	if o.MaxConns > 0 {
		cfg.MaxConns = o.MaxConns
	}
	cfg.ConnConfig.RuntimeParams["application_name"] = "vigo-core"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("db: %w", err)
	}
	d := &DB{pool: pool, connCfg: cfg.ConnConfig, keys: o.Keyring, lockClass: o.LockClass}
	if d.lockClass == 0 {
		d.lockClass = DefaultLockClass
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("db: %w", err)
	}
	return d, nil
}

func (d *DB) Close() { d.pool.Close() }

func (d *DB) Ping(ctx context.Context) error { return d.pool.Ping(ctx) }

// Pool exposes the pool, for tests.
func (d *DB) Pool() *pgxpool.Pool { return d.pool }

// Dedicated opens a connection outside the pool. Session-level advisory
// locks live on it, and are released when it closes or dies.
//
// Server-side TCP keepalives make Postgres drop the session, and so release
// its locks, within about 25 seconds of the peer vanishing, instead of the
// kernel default of hours.
func (d *DB) Dedicated(ctx context.Context) (*pgx.Conn, error) {
	cfg := d.connCfg.Copy()
	cfg.RuntimeParams["tcp_keepalives_idle"] = "10"
	cfg.RuntimeParams["tcp_keepalives_interval"] = "5"
	cfg.RuntimeParams["tcp_keepalives_count"] = "3"
	c, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("db: %w", err)
	}
	return c, nil
}
