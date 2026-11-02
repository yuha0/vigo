package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/yuha0/vigo/internal/secretbox"
	"github.com/yuha0/vigo/internal/tv"
)

var tokenAAD = secretbox.AAD("tv_credentials", "token", "1")

// TVStore implements tv.Store.
type TVStore struct{ d *DB }

var _ tv.Store = (*TVStore)(nil)

func (d *DB) TV() *TVStore { return &TVStore{d: d} }

func (s *TVStore) Config(ctx context.Context) (tv.Config, error) {
	var c tv.Config
	err := s.d.pool.QueryRow(ctx, `SELECT tv_host, tv_port, client_name FROM config WHERE id = 1`).
		Scan(&c.Host, &c.Port, &c.ClientName)
	if errors.Is(err, pgx.ErrNoRows) {
		return tv.Config{}, nil
	}
	if err != nil {
		return tv.Config{}, fmt.Errorf("db: read config: %w", err)
	}
	return c, nil
}

func (s *TVStore) SetConfig(ctx context.Context, c tv.Config) error {
	_, err := s.d.pool.Exec(ctx, `
		INSERT INTO config (id, tv_host, tv_port, client_name) VALUES (1, $1, $2, $3)
		ON CONFLICT (id) DO UPDATE
		SET tv_host = $1, tv_port = $2, client_name = $3, updated_at = now()`,
		c.Host, c.Port, c.ClientName)
	if err != nil {
		return fmt.Errorf("db: write config: %w", err)
	}
	return nil
}

func (s *TVStore) Token(ctx context.Context) (string, error) {
	var sealed secretbox.Sealed
	err := s.d.pool.QueryRow(ctx, `SELECT key_id, nonce, ciphertext FROM tv_credentials WHERE id = 1`).
		Scan(&sealed.KeyID, &sealed.Nonce, &sealed.Ciphertext)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("db: read token: %w", err)
	}
	b, err := s.d.keys.Open(sealed, tokenAAD)
	if err != nil {
		return "", fmt.Errorf("db: decrypt token: %w", err)
	}
	return string(b), nil
}

func (s *TVStore) SetToken(ctx context.Context, token, clientName string) error {
	sealed, err := s.d.keys.Seal([]byte(token), tokenAAD)
	if err != nil {
		return err
	}
	_, err = s.d.pool.Exec(ctx, `
		INSERT INTO tv_credentials (id, key_id, nonce, ciphertext, client_name) VALUES (1, $1, $2, $3, $4)
		ON CONFLICT (id) DO UPDATE
		SET key_id = $1, nonce = $2, ciphertext = $3, client_name = $4,
		    created_at = now(), last_verified_at = NULL, invalidated_at = NULL`,
		sealed.KeyID, sealed.Nonce, sealed.Ciphertext, clientName)
	if err != nil {
		return fmt.Errorf("db: write token: %w", err)
	}
	return nil
}

func (s *TVStore) TokenVerified(ctx context.Context) error {
	_, err := s.d.pool.Exec(ctx, `UPDATE tv_credentials SET last_verified_at = now(), invalidated_at = NULL WHERE id = 1`)
	return err
}

func (s *TVStore) TokenRejected(ctx context.Context) error {
	_, err := s.d.pool.Exec(ctx, `UPDATE tv_credentials SET invalidated_at = now() WHERE id = 1 AND invalidated_at IS NULL`)
	return err
}

func (s *TVStore) Selection(ctx context.Context) ([]byte, error) {
	var b []byte
	err := s.d.pool.QueryRow(ctx, `SELECT record FROM selection WHERE id = 1`).Scan(&b)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("db: read selection: %w", err)
	}
	return b, nil
}

func (s *TVStore) SetSelection(ctx context.Context, record []byte) error {
	var err error
	if record == nil {
		_, err = s.d.pool.Exec(ctx, `DELETE FROM selection WHERE id = 1`)
	} else {
		_, err = s.d.pool.Exec(ctx, `
			INSERT INTO selection (id, record) VALUES (1, $1::jsonb)
			ON CONFLICT (id) DO UPDATE SET record = $1::jsonb, updated_at = now()`, string(record))
	}
	if err != nil {
		return fmt.Errorf("db: write selection: %w", err)
	}
	return nil
}

func (s *TVStore) Uploads(ctx context.Context) (map[string]int64, error) {
	rows, err := s.d.pool.Query(ctx, `SELECT content_id, bytes FROM uploads`)
	if err != nil {
		return nil, fmt.Errorf("db: read uploads: %w", err)
	}
	out := map[string]int64{}
	var id string
	var n int64
	_, err = pgx.ForEachRow(rows, []any{&id, &n}, func() error {
		out[id] = n
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("db: read uploads: %w", err)
	}
	return out, nil
}

func (s *TVStore) SetUpload(ctx context.Context, contentID string, n int64) error {
	_, err := s.d.pool.Exec(ctx, `
		INSERT INTO uploads (content_id, bytes) VALUES ($1, $2)
		ON CONFLICT (content_id) DO UPDATE SET bytes = $2, uploaded_at = now()`, contentID, n)
	return err
}

func (s *TVStore) DeleteUpload(ctx context.Context, contentID string) error {
	_, err := s.d.pool.Exec(ctx, `DELETE FROM uploads WHERE content_id = $1`, contentID)
	return err
}

func (s *TVStore) ObserveModel(ctx context.Context, model string) (tv.ModelObservation, error) {
	var obs tv.ModelObservation
	err := pgx.BeginFunc(ctx, s.d.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `LOCK TABLE model_history IN SHARE ROW EXCLUSIVE MODE`); err != nil {
			return err
		}
		err := tx.QueryRow(ctx, `
			INSERT INTO model_history (model, acknowledged_at)
			VALUES ($1, CASE WHEN NOT EXISTS (SELECT 1 FROM model_history) THEN now() END)
			ON CONFLICT (model) DO UPDATE SET last_seen = now()
			RETURNING acknowledged_at IS NOT NULL`, model).Scan(&obs.Acknowledged)
		if err != nil {
			return err
		}
		err = tx.QueryRow(ctx, `SELECT model FROM model_history WHERE model <> $1 ORDER BY last_seen DESC LIMIT 1`, model).
			Scan(&obs.Previous)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	})
	if err != nil {
		return tv.ModelObservation{}, fmt.Errorf("db: record model: %w", err)
	}
	return obs, nil
}

// AcknowledgeModels acknowledges every model seen so far, clearing the
// model-change warning. It returns how many were newly acknowledged.
func (d *DB) AcknowledgeModels(ctx context.Context) (int64, error) {
	tag, err := d.pool.Exec(ctx, `UPDATE model_history SET acknowledged_at = now() WHERE acknowledged_at IS NULL`)
	if err != nil {
		return 0, fmt.Errorf("db: acknowledge models: %w", err)
	}
	return tag.RowsAffected(), nil
}
