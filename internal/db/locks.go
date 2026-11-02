package db

import (
	"context"
	"fmt"
	"hash/fnv"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	keyMigrations int32 = 0
	keyCoreLeader int32 = 1

	releaseTimeout = 2 * time.Second
)

// Lock identifies a session-level advisory lock.
type Lock struct {
	Class int32
	Key   int32
}

// CoreLeaderLock is held by the vigo-core leader.
func (d *DB) CoreLeaderLock() Lock { return Lock{d.lockClass, keyCoreLeader} }

// IntegrationLock is held on behalf of the leader of an integration kind.
func (d *DB) IntegrationLock(kind string) Lock {
	h := fnv.New32a()
	h.Write([]byte(kind))
	return Lock{d.lockClass + 1, int32(h.Sum32() & 0x7fffffff)}
}

// Release gives up l and closes conn. Closing alone also releases the lock,
// but only once the server has noticed the closed connection; unlocking first
// frees it for the next holder before Release returns.
func Release(conn *pgx.Conn, l Lock) {
	ctx, cancel := context.WithTimeout(context.Background(), releaseTimeout)
	defer cancel()
	_, _ = conn.Exec(ctx, `SELECT pg_advisory_unlock($1, $2)`, l.Class, l.Key)
	_ = conn.Close(ctx)
}

// TryLock takes l on conn without waiting.
func TryLock(ctx context.Context, conn *pgx.Conn, l Lock) (bool, error) {
	var ok bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1, $2)`, l.Class, l.Key).Scan(&ok); err != nil {
		return false, fmt.Errorf("db: advisory lock: %w", err)
	}
	return ok, nil
}

// heldBy is a SQL condition true when the session with pid (an SQL
// expression) holds the two-key advisory lock ($classParam, $keyParam) in
// this database. Rows naming a session thereby expire the moment it dies.
func heldBy(pid string, classParam, keyParam int) string {
	return fmt.Sprintf(`EXISTS (SELECT 1 FROM pg_locks k
		WHERE k.locktype = 'advisory'
		  AND k.database = (SELECT oid FROM pg_database WHERE datname = current_database())
		  AND k.classid::bigint = $%d AND k.objid::bigint = $%d AND k.objsubid = 2
		  AND k.pid = %s AND k.granted)`, classParam, keyParam, pid)
}
