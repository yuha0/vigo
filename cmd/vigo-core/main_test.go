package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yuha0/vigo/internal/db/dbtest"
	"github.com/yuha0/vigo/internal/secretbox"
)

func TestBindEnv(t *testing.T) {
	t.Setenv("VIGO_TV_HOST", "192.0.2.10")
	t.Setenv("VIGO_LEADER_ELECT", "true")
	t.Setenv("VIGO_POLL_INTERVAL", "3s")
	t.Setenv("VIGO_LISTEN", ":9999")
	t.Setenv("VIGO_DATABASE_URL_FILE", "/run/db/uri")

	cmd := newCommand()
	f := cmd.Flags()
	f.AddFlagSet(cmd.PersistentFlags())
	if err := f.Parse([]string{"--listen", ":8080"}); err != nil {
		t.Fatal(err)
	}
	if err := bindEnv(f); err != nil {
		t.Fatal(err)
	}
	tests := map[string]string{
		"tv-host":           "192.0.2.10",
		"leader-elect":      "true",
		"poll-interval":     (3 * time.Second).String(),
		"listen":            ":8080",
		"tv-port":           "8002",
		"listen-internal":   ":8443",
		"listen-ops":        ":9090",
		"database-url-file": "/run/db/uri",
	}
	for name, want := range tests {
		if got := f.Lookup(name).Value.String(); got != want {
			t.Errorf("--%s = %q, want %q", name, got, want)
		}
	}

	t.Setenv("VIGO_TV_PORT", "eighty")
	if err := bindEnv(newCommand().Flags()); err == nil {
		t.Error("invalid VIGO_TV_PORT accepted")
	}
}

func TestParseToken(t *testing.T) {
	tests := []struct {
		in, token, name string
		ok              bool
	}{
		{in: "12345678\n", token: "12345678", ok: true},
		{in: `{"host":"192.0.2.10","name":"vigo","token":"87654321"}`, token: "87654321", name: "vigo", ok: true},
		{in: `{"token":""}`},
		{in: ""},
		{in: "two words"},
		{in: "{not json"},
	}
	for _, tt := range tests {
		token, name, err := parseToken([]byte(tt.in))
		if (err == nil) != tt.ok || token != tt.token || name != tt.name {
			t.Errorf("parseToken(%q) = %q, %q, %v", tt.in, token, name, err)
		}
	}
}

func TestAdvertiseAddress(t *testing.T) {
	t.Setenv("POD_IP", "198.51.100.21")
	got, err := advertiseAddress(options{listenInternal: ":8443"})
	if err != nil || got != "198.51.100.21:8443" {
		t.Errorf("= %q, %v", got, err)
	}
	got, _ = advertiseAddress(options{listenInternal: ":8443", advertiseAddress: "core-0.vigo:9443"})
	if got != "core-0.vigo:9443" {
		t.Errorf("explicit = %q", got)
	}
}

func run(t *testing.T, args ...string) string {
	t.Helper()
	cmd := newCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(args)
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("%v: %v", args, err)
	}
	return out.String()
}

func TestKeyringCommands(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keyring.json")
	run(t, "keyring", "new", "--out", path)
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v", fi.Mode().Perm())
	}
	run(t, "keyring", "rotate", "--file", path, "--id", "k2")
	k, err := secretbox.LoadKeyring(path)
	if err != nil || k.Primary() != "k2" || len(k.KeyIDs()) != 2 {
		t.Errorf("keyring = %v, %v", k, err)
	}
	cmd := newCommand()
	cmd.SetArgs([]string{"keyring", "new", "--out", path})
	if err := cmd.Execute(); err == nil {
		t.Error("keyring new overwrote an existing file")
	}
}

func TestImportTokenAndAckModel(t *testing.T) {
	e := dbtest.Open(t)
	dir := t.TempDir()
	write := func(name string, b []byte) string {
		p := filepath.Join(dir, name)
		os.WriteFile(p, b, 0o600)
		return p
	}
	o := []string{
		"--database-url-file", write("url", []byte(os.Getenv(dbtest.EnvURL))),
		"--keyring-file", write("keyring.json", e.Keyring),
	}
	tokenFile := write("vigo.token", []byte(`{"name":"vigo","token":"11223344"}`))

	// The command opens the default search path, not the test schema, so
	// point the test's schema at it through the URL.
	urlWithSchema := os.Getenv(dbtest.EnvURL)
	sep := "?"
	if strings.Contains(urlWithSchema, "?") {
		sep = "&"
	}
	write("url", []byte(urlWithSchema+sep+"search_path="+e.Options.Schema))

	out := run(t, append([]string{"import-token", "--file", tokenFile}, o...)...)
	if strings.Contains(out, "11223344") {
		t.Error("the token was printed")
	}
	if tok, err := e.DB.TV().Token(context.Background()); err != nil || tok != "11223344" {
		t.Errorf("stored token = %q, %v", tok, err)
	}

	e.DB.TV().ObserveModel(context.Background(), "24_PONTUSM_FTV")
	e.DB.TV().ObserveModel(context.Background(), "24_PTM_FTV_T09")
	if out := run(t, append([]string{"ack-model"}, o...)...); !strings.Contains(out, "Acknowledged 1") {
		t.Errorf("ack-model: %s", out)
	}
}

func TestResetPassword(t *testing.T) {
	e := dbtest.Open(t)
	ctx := context.Background()
	if _, err := e.DB.Pool().Exec(ctx, `INSERT INTO web_password (hash) VALUES ('$argon2id$v=19$m=1,t=1,p=1$AA$AA')`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.DB.Pool().Exec(ctx, `INSERT INTO web_sessions (token_hash, expires_at) VALUES ('\x01', now() + interval '1 day'), ('\x02', now() + interval '1 day')`); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	url := os.Getenv(dbtest.EnvURL)
	sep := "?"
	if strings.Contains(url, "?") {
		sep = "&"
	}
	urlFile := filepath.Join(dir, "url")
	keyFile := filepath.Join(dir, "keyring.json")
	os.WriteFile(urlFile, []byte(url+sep+"search_path="+e.Options.Schema), 0o600)
	os.WriteFile(keyFile, e.Keyring, 0o600)

	out := run(t, "reset-password", "--database-url-file", urlFile, "--keyring-file", keyFile)
	if !strings.Contains(out, "next visitor") {
		t.Errorf("output = %q", out)
	}
	var passwords, sessions int
	e.DB.Pool().QueryRow(ctx, `SELECT (SELECT count(*) FROM web_password), (SELECT count(*) FROM web_sessions)`).Scan(&passwords, &sessions)
	if passwords != 0 || sessions != 0 {
		t.Errorf("after reset: %d passwords, %d sessions", passwords, sessions)
	}
}
