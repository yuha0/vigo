// Package dbtest opens an isolated database for tests. Tests are skipped
// unless VIGO_TEST_DATABASE_URL points at a PostgreSQL 18 database.
package dbtest

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/yuha0/vigo/internal/db"
	"github.com/yuha0/vigo/internal/secretbox"
)

const EnvURL = "VIGO_TEST_DATABASE_URL"

type Env struct {
	DB      *db.DB
	Options db.Options
	Keyring []byte
}

// Keyring returns a new single-key keyring file content.
func Keyring(t testing.TB) []byte {
	t.Helper()
	b, err := secretbox.AddPrimary(nil, "k1")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Open creates a fresh schema and advisory-lock namespace, migrates it and
// drops it when the test ends.
func Open(t testing.TB) *Env {
	t.Helper()
	url := os.Getenv(EnvURL)
	if url == "" {
		t.Skipf("%s is not set; point it at a PostgreSQL 18 database", EnvURL)
	}
	ctx := context.Background()
	var raw [6]byte
	rand.Read(raw[:])
	schema := "test_" + hex.EncodeToString(raw[:])

	admin, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, err := pgx.Connect(context.Background(), url)
		if err != nil {
			return
		}
		defer c.Close(context.Background())
		c.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
	})

	kr := Keyring(t)
	keys, err := secretbox.ParseKeyring(kr)
	if err != nil {
		t.Fatal(err)
	}
	opts := db.Options{
		URL:       url,
		Keyring:   keys,
		Schema:    schema,
		LockClass: int32(binary.BigEndian.Uint32(raw[:4]) & 0x3fffffff),
	}
	d, err := db.Open(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	if _, err := d.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return &Env{DB: d, Options: opts, Keyring: kr}
}

// Reopen opens the same schema with another keyring.
func (e *Env) Reopen(t testing.TB, keyring []byte) *db.DB {
	t.Helper()
	keys, err := secretbox.ParseKeyring(keyring)
	if err != nil {
		t.Fatal(err)
	}
	o := e.Options
	o.Keyring = keys
	d, err := db.Open(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	return d
}
