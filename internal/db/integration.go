package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/yuha0/vigo/internal/secretbox"
)

var (
	// ErrNotLeader means the lease is not the current leadership of its
	// kind: superseded, or its session has died.
	ErrNotLeader = errors.New("db: not the current integration leader")
	// ErrVersionMismatch means a conditional write found another version.
	ErrVersionMismatch = errors.New("db: version mismatch")
)

func stateAAD(kind, key string) []byte {
	return secretbox.AAD("integration_state", "value", kind+"/"+key)
}

// ClaimIntegration records conn's session as the leader of kind. conn must
// hold IntegrationLock(kind). The previous leader's lease and endpoint are
// cleared; writes in flight under the old lease finish first, because they
// hold the row FOR SHARE.
func (d *DB) ClaimIntegration(ctx context.Context, conn *pgx.Conn, kind, instance, identity string, leaseHash []byte) error {
	_, err := conn.Exec(ctx, `
		INSERT INTO integrations (kind, instance, identity, lease_hash, backend_pid, endpoint, last_seen)
		VALUES ($1, $2, $3, $4, pg_backend_pid(), '', now())
		ON CONFLICT (kind) DO UPDATE
		SET instance = $2, identity = $3, lease_hash = $4, backend_pid = pg_backend_pid(), endpoint = '', last_seen = now()`,
		kind, instance, identity, leaseHash)
	if err != nil {
		return fmt.Errorf("db: claim integration: %w", err)
	}
	return nil
}

// TouchIntegration updates last_seen while a leader stream is held.
func (d *DB) TouchIntegration(ctx context.Context, kind string, leaseHash []byte) error {
	_, err := d.pool.Exec(ctx, `UPDATE integrations SET last_seen = now() WHERE kind = $1 AND lease_hash = $2`, kind, leaseHash)
	return err
}

// leaseCondition is true for the row of kind $1 whose lease is $2 and whose
// session still holds the lock ($3, $4).
var leaseCondition = `kind = $1 AND lease_hash = $2 AND ` + heldBy("backend_pid", 3, 4)

func (d *DB) leaseArgs(kind string, leaseHash []byte) []any {
	l := d.IntegrationLock(kind)
	return []any{kind, leaseHash, l.Class, l.Key}
}

// RegisterEndpoint records where vigo-core reaches the leader of kind.
func (d *DB) RegisterEndpoint(ctx context.Context, kind string, leaseHash []byte, endpoint string) error {
	args := append(d.leaseArgs(kind, leaseHash), endpoint)
	tag, err := d.pool.Exec(ctx, `UPDATE integrations SET endpoint = $5, last_seen = now() WHERE `+leaseCondition, args...)
	if err != nil {
		return fmt.Errorf("db: register endpoint: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotLeader
	}
	return nil
}

// IntegrationEndpoint is the endpoint registered by the current leader of
// kind, or ErrNotFound.
func (d *DB) IntegrationEndpoint(ctx context.Context, kind string) (string, error) {
	l := d.IntegrationLock(kind)
	var ep string
	err := d.pool.QueryRow(ctx, `SELECT endpoint FROM integrations WHERE kind = $1 AND endpoint <> '' AND `+heldBy("backend_pid", 2, 3),
		kind, l.Class, l.Key).Scan(&ep)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("db: read endpoint: %w", err)
	}
	return ep, nil
}

type StateItem struct {
	Key     string
	Value   []byte
	Version int64
}

func (d *DB) ListState(ctx context.Context, kind string) ([]StateItem, error) {
	rows, err := d.pool.Query(ctx, `SELECT key, key_id, nonce, ciphertext, version FROM integration_state WHERE kind = $1 ORDER BY key`, kind)
	if err != nil {
		return nil, fmt.Errorf("db: list state: %w", err)
	}
	var (
		out    []StateItem
		key    string
		sealed secretbox.Sealed
		ver    int64
	)
	_, err = pgx.ForEachRow(rows, []any{&key, &sealed.KeyID, &sealed.Nonce, &sealed.Ciphertext, &ver}, func() error {
		v, err := d.keys.Open(sealed, stateAAD(kind, key))
		if err != nil {
			return fmt.Errorf("decrypt %s/%s: %w", kind, key, err)
		}
		out = append(out, StateItem{Key: key, Value: v, Version: ver})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("db: list state: %w", err)
	}
	return out, nil
}

// withLease runs fn in a transaction that holds the lease row FOR SHARE, so
// a new leader's claim waits for it.
func (d *DB) withLease(ctx context.Context, kind string, leaseHash []byte, fn func(pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, d.pool, func(tx pgx.Tx) error {
		var one int
		err := tx.QueryRow(ctx, `SELECT 1 FROM integrations WHERE `+leaseCondition+` FOR SHARE`, d.leaseArgs(kind, leaseHash)...).Scan(&one)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotLeader
		}
		if err != nil {
			return err
		}
		return fn(tx)
	})
}

// PutState writes one item. A non-zero expected version makes the write
// conditional on the stored version.
func (d *DB) PutState(ctx context.Context, kind string, leaseHash []byte, key string, value []byte, expected int64) (int64, error) {
	sealed, err := d.keys.Seal(value, stateAAD(kind, key))
	if err != nil {
		return 0, err
	}
	var version int64
	err = d.withLease(ctx, kind, leaseHash, func(tx pgx.Tx) error {
		var err error
		if expected == 0 {
			err = tx.QueryRow(ctx, `
				INSERT INTO integration_state (kind, key, key_id, nonce, ciphertext, version)
				VALUES ($1, $2, $3, $4, $5, 1)
				ON CONFLICT (kind, key) DO UPDATE
				SET key_id = $3, nonce = $4, ciphertext = $5,
				    version = integration_state.version + 1, updated_at = now()
				RETURNING version`,
				kind, key, sealed.KeyID, sealed.Nonce, sealed.Ciphertext).Scan(&version)
		} else {
			err = tx.QueryRow(ctx, `
				UPDATE integration_state
				SET key_id = $3, nonce = $4, ciphertext = $5, version = version + 1, updated_at = now()
				WHERE kind = $1 AND key = $2 AND version = $6
				RETURNING version`,
				kind, key, sealed.KeyID, sealed.Nonce, sealed.Ciphertext, expected).Scan(&version)
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrVersionMismatch
			}
		}
		return err
	})
	if err != nil {
		if errors.Is(err, ErrNotLeader) || errors.Is(err, ErrVersionMismatch) {
			return 0, err
		}
		return 0, fmt.Errorf("db: put state: %w", err)
	}
	return version, nil
}

func (d *DB) DeleteState(ctx context.Context, kind string, leaseHash []byte, key string) error {
	err := d.withLease(ctx, kind, leaseHash, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM integration_state WHERE kind = $1 AND key = $2`, kind, key)
		return err
	})
	if err != nil && !errors.Is(err, ErrNotLeader) {
		return fmt.Errorf("db: delete state: %w", err)
	}
	return err
}
