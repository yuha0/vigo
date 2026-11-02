package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/yuha0/vigo/internal/secretbox"
)

// Reencrypt rewrites every encrypted value not sealed with the primary key.
// Each update is conditional on the old nonce, so a concurrent write wins
// and is left alone. Values that cannot be decrypted are reported, not
// changed.
func (d *DB) Reencrypt(ctx context.Context) (rewritten int, err error) {
	primary := d.keys.Primary()
	var errs []error

	var tok secretbox.Sealed
	qerr := d.pool.QueryRow(ctx, `SELECT key_id, nonce, ciphertext FROM tv_credentials WHERE id = 1 AND key_id <> $1`, primary).
		Scan(&tok.KeyID, &tok.Nonce, &tok.Ciphertext)
	switch {
	case errors.Is(qerr, pgx.ErrNoRows):
	case qerr != nil:
		errs = append(errs, qerr)
	default:
		if n, err := d.reseal(tok, tokenAAD, func(s secretbox.Sealed) (int64, error) {
			tag, err := d.pool.Exec(ctx, `UPDATE tv_credentials SET key_id = $1, nonce = $2, ciphertext = $3 WHERE id = 1 AND nonce = $4`,
				s.KeyID, s.Nonce, s.Ciphertext, tok.Nonce)
			return tag.RowsAffected(), err
		}); err != nil {
			errs = append(errs, fmt.Errorf("tv token: %w", err))
		} else {
			rewritten += n
		}
	}

	type row struct {
		kind, key string
		sealed    secretbox.Sealed
	}
	rows, qerr := d.pool.Query(ctx, `SELECT kind, key, key_id, nonce, ciphertext FROM integration_state WHERE key_id <> $1`, primary)
	if qerr != nil {
		return rewritten, errors.Join(append(errs, qerr)...)
	}
	var stale []row
	var r row
	_, qerr = pgx.ForEachRow(rows, []any{&r.kind, &r.key, &r.sealed.KeyID, &r.sealed.Nonce, &r.sealed.Ciphertext}, func() error {
		stale = append(stale, r)
		return nil
	})
	if qerr != nil {
		errs = append(errs, qerr)
	}
	for _, r := range stale {
		n, err := d.reseal(r.sealed, stateAAD(r.kind, r.key), func(s secretbox.Sealed) (int64, error) {
			tag, err := d.pool.Exec(ctx, `UPDATE integration_state SET key_id = $1, nonce = $2, ciphertext = $3
				WHERE kind = $4 AND key = $5 AND nonce = $6`,
				s.KeyID, s.Nonce, s.Ciphertext, r.kind, r.key, r.sealed.Nonce)
			return tag.RowsAffected(), err
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("integration state %s/%s: %w", r.kind, r.key, err))
			continue
		}
		rewritten += n
	}
	return rewritten, errors.Join(errs...)
}

func (d *DB) reseal(old secretbox.Sealed, aad []byte, write func(secretbox.Sealed) (int64, error)) (int, error) {
	pt, err := d.keys.Open(old, aad)
	if err != nil {
		return 0, err
	}
	s, err := d.keys.Seal(pt, aad)
	if err != nil {
		return 0, err
	}
	n, err := write(s)
	return int(n), err
}
