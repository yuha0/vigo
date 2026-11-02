package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Leader is the vigo-core replica holding the leader lock.
type Leader struct {
	Identity string
	// Address is its mTLS listener, host:port.
	Address string
}

// SetLeader records conn's session as leader. conn must hold
// CoreLeaderLock.
func (d *DB) SetLeader(ctx context.Context, conn *pgx.Conn, identity, address string) error {
	_, err := conn.Exec(ctx, `
		INSERT INTO leader (id, identity, address, backend_pid) VALUES (1, $1, $2, pg_backend_pid())
		ON CONFLICT (id) DO UPDATE
		SET identity = $1, address = $2, backend_pid = pg_backend_pid(), since = now()`,
		identity, address)
	if err != nil {
		return fmt.Errorf("db: record leader: %w", err)
	}
	return nil
}

// CurrentLeader returns the leader if the recorded session still holds the
// lock, and ErrNotFound otherwise.
func (d *DB) CurrentLeader(ctx context.Context) (Leader, error) {
	l := d.CoreLeaderLock()
	var out Leader
	err := d.pool.QueryRow(ctx, `SELECT identity, address FROM leader WHERE id = 1 AND `+heldBy("backend_pid", 1, 2),
		l.Class, l.Key).Scan(&out.Identity, &out.Address)
	if errors.Is(err, pgx.ErrNoRows) {
		return Leader{}, ErrNotFound
	}
	if err != nil {
		return Leader{}, fmt.Errorf("db: read leader: %w", err)
	}
	return out, nil
}
