package db

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"slices"
	"strconv"
	"strings"
)

//go:embed migrations/*.sql
var migrations embed.FS

type migration struct {
	version int
	name    string
	sql     string
}

func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrations, "migrations")
	if err != nil {
		return nil, err
	}
	var out []migration
	for _, e := range entries {
		prefix, _, ok := strings.Cut(e.Name(), "_")
		v, err := strconv.Atoi(prefix)
		if !ok || err != nil {
			return nil, fmt.Errorf("db: migration %s must be named NNNN_name.sql", e.Name())
		}
		b, err := migrations.ReadFile("migrations/" + e.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, migration{version: v, name: e.Name(), sql: string(b)})
	}
	slices.SortFunc(out, func(a, b migration) int { return a.version - b.version })
	return out, nil
}

// SchemaVersion is the newest migration embedded in this binary.
func SchemaVersion() int {
	m, err := loadMigrations()
	if err != nil || len(m) == 0 {
		return 0
	}
	return m[len(m)-1].version
}

// Migrate applies pending migrations in one transaction, serialized by an
// advisory lock. The leader runs it after winning election.
func (d *DB) Migrate(ctx context.Context) (applied []string, err error) {
	ms, err := loadMigrations()
	if err != nil {
		return nil, err
	}
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("db: migrate: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1, 0)`, d.lockClass); err != nil {
		return nil, fmt.Errorf("db: migrate: %w", err)
	}
	if _, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    integer     PRIMARY KEY,
		name       text        NOT NULL,
		applied_at timestamptz NOT NULL DEFAULT now()
	)`); err != nil {
		return nil, fmt.Errorf("db: migrate: %w", err)
	}
	rows, err := tx.Query(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("db: migrate: %w", err)
	}
	done := map[int]bool{}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		done[v] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, m := range ms {
		if done[m.version] {
			continue
		}
		if _, err := tx.Exec(ctx, m.sql); err != nil {
			return nil, fmt.Errorf("db: migration %s: %w", m.name, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version, name) VALUES ($1, $2)`, m.version, m.name); err != nil {
			return nil, err
		}
		applied = append(applied, m.name)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("db: migrate: %w", err)
	}
	return applied, nil
}
