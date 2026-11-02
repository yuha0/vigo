package db_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/yuha0/vigo/internal/db"
	"github.com/yuha0/vigo/internal/db/dbtest"
	"github.com/yuha0/vigo/internal/pki/pkitest"
	"github.com/yuha0/vigo/internal/secretbox"
	"github.com/yuha0/vigo/internal/tv"
)

func TestMigrateIsIdempotent(t *testing.T) {
	e := dbtest.Open(t)
	applied, err := e.DB.Migrate(context.Background())
	if err != nil || len(applied) != 0 {
		t.Errorf("second migrate applied %v, %v", applied, err)
	}
	if db.SchemaVersion() < 1 {
		t.Error("no embedded migrations")
	}
}

func TestTVStore(t *testing.T) {
	e := dbtest.Open(t)
	ctx := context.Background()
	s := e.DB.TV()

	if c, err := s.Config(ctx); err != nil || c != (tv.Config{}) {
		t.Errorf("empty config = %+v, %v", c, err)
	}
	want := tv.Config{Host: "192.0.2.10", Port: 8002, ClientName: "vigo"}
	if err := s.SetConfig(ctx, want); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.Config(ctx); c != want {
		t.Errorf("config = %+v", c)
	}

	if tok, err := s.Token(ctx); err != nil || tok != "" {
		t.Errorf("empty token = %q, %v", tok, err)
	}
	if err := s.SetToken(ctx, "12345678", "vigo"); err != nil {
		t.Fatal(err)
	}
	if tok, _ := s.Token(ctx); tok != "12345678" {
		t.Errorf("token = %q", tok)
	}
	var raw []byte
	e.DB.Pool().QueryRow(ctx, `SELECT ciphertext FROM tv_credentials`).Scan(&raw)
	if bytes.Contains(raw, []byte("12345678")) {
		t.Error("token stored in plaintext")
	}
	s.TokenRejected(ctx)
	var invalidated, verified bool
	e.DB.Pool().QueryRow(ctx, `SELECT invalidated_at IS NOT NULL, last_verified_at IS NOT NULL FROM tv_credentials`).Scan(&invalidated, &verified)
	if !invalidated || verified {
		t.Errorf("after rejection: invalidated=%v verified=%v", invalidated, verified)
	}
	s.TokenVerified(ctx)
	e.DB.Pool().QueryRow(ctx, `SELECT invalidated_at IS NOT NULL, last_verified_at IS NOT NULL FROM tv_credentials`).Scan(&invalidated, &verified)
	if invalidated || !verified {
		t.Errorf("after verification: invalidated=%v verified=%v", invalidated, verified)
	}

	if sel, err := s.Selection(ctx); err != nil || sel != nil {
		t.Errorf("empty selection = %s, %v", sel, err)
	}
	if err := s.SetSelection(ctx, []byte(`{"content_id":"SAM-S2701","reported":"SAM-S2701"}`)); err != nil {
		t.Fatal(err)
	}
	if sel, _ := s.Selection(ctx); !bytes.Contains(sel, []byte(`"content_id": "SAM-S2701"`)) {
		t.Errorf("selection = %s", sel)
	}
	s.SetSelection(ctx, nil)
	if sel, _ := s.Selection(ctx); sel != nil {
		t.Errorf("selection after delete = %s", sel)
	}

	s.SetUpload(ctx, "MY_F0003", 1234)
	s.SetUpload(ctx, "MY_F0004", 99)
	s.DeleteUpload(ctx, "MY_F0004")
	if u, _ := s.Uploads(ctx); len(u) != 1 || u["MY_F0003"] != 1234 {
		t.Errorf("uploads = %v", u)
	}
}

func TestObserveModel(t *testing.T) {
	e := dbtest.Open(t)
	ctx := context.Background()
	s := e.DB.TV()
	steps := []struct {
		model    string
		ack      bool
		previous string
	}{
		{"24_PONTUSM_FTV", true, ""},
		{"24_PONTUSM_FTV", true, ""},
		{"24_PTM_FTV_T09", false, "24_PONTUSM_FTV"},
		{"24_PTM_FTV_T09", false, "24_PONTUSM_FTV"},
	}
	for i, st := range steps {
		obs, err := s.ObserveModel(ctx, st.model)
		if err != nil {
			t.Fatal(err)
		}
		if obs.Acknowledged != st.ack || obs.Previous != st.previous {
			t.Errorf("step %d: %+v", i, obs)
		}
	}
	if n, err := e.DB.AcknowledgeModels(ctx); err != nil || n != 1 {
		t.Errorf("acknowledged %d, %v", n, err)
	}
	if obs, _ := s.ObserveModel(ctx, "24_PTM_FTV_T09"); !obs.Acknowledged {
		t.Error("still unacknowledged")
	}
}

func TestLeaderRecord(t *testing.T) {
	e := dbtest.Open(t)
	ctx := context.Background()
	if _, err := e.DB.CurrentLeader(ctx); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	conn, err := e.DB.Dedicated(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := db.TryLock(ctx, conn, e.DB.CoreLeaderLock()); !ok || err != nil {
		t.Fatalf("lock: %v %v", ok, err)
	}
	other, _ := e.DB.Dedicated(ctx)
	defer other.Close(ctx)
	if ok, _ := db.TryLock(ctx, other, e.DB.CoreLeaderLock()); ok {
		t.Fatal("second session took the lock")
	}
	if err := e.DB.SetLeader(ctx, conn, "core-0", "10.0.0.5:8443"); err != nil {
		t.Fatal(err)
	}
	l, err := e.DB.CurrentLeader(ctx)
	if err != nil || l != (db.Leader{Identity: "core-0", Address: "10.0.0.5:8443"}) {
		t.Fatalf("leader = %+v, %v", l, err)
	}
	pid := conn.PgConn().PID()
	conn.Close(ctx)
	e.WaitSessionGone(t, pid)
	if _, err := e.DB.CurrentLeader(ctx); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("leader after its session closed: err = %v", err)
	}
	if ok, _ := db.TryLock(ctx, other, e.DB.CoreLeaderLock()); !ok {
		t.Error("lock not released with the session")
	}
}

func TestIntegrationState(t *testing.T) {
	e := dbtest.Open(t)
	ctx := context.Background()
	lease := []byte("lease-1")

	conn, err := e.DB.Dedicated(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if _, err := e.DB.PutState(ctx, "homekit", lease, "a", []byte("x"), 0); !errors.Is(err, db.ErrNotLeader) {
		t.Fatalf("write before claiming: %v", err)
	}
	if ok, _ := db.TryLock(ctx, conn, e.DB.IntegrationLock("homekit")); !ok {
		t.Fatal("lock")
	}
	if err := e.DB.ClaimIntegration(ctx, conn, "homekit", "home-0", "spiffe://vigo/integration/homekit", lease); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		key      string
		value    string
		expected int64
		lease    []byte
		want     int64
		wantErr  error
	}{
		{name: "create", key: "hap.AccessoryInfo", value: "v1", want: 1},
		{name: "overwrite", key: "hap.AccessoryInfo", value: "v2", want: 2},
		{name: "conditional ok", key: "hap.AccessoryInfo", value: "v3", expected: 2, want: 3},
		{name: "conditional stale", key: "hap.AccessoryInfo", value: "v4", expected: 2, wantErr: db.ErrVersionMismatch},
		{name: "conditional missing", key: "nope", value: "x", expected: 1, wantErr: db.ErrVersionMismatch},
		{name: "wrong lease", key: "b", value: "x", lease: []byte("other"), wantErr: db.ErrNotLeader},
		{name: "second key", key: "hap.IdentifierCache", value: "c", want: 1},
	}
	for _, tt := range tests {
		l := lease
		if tt.lease != nil {
			l = tt.lease
		}
		v, err := e.DB.PutState(ctx, "homekit", l, tt.key, []byte(tt.value), tt.expected)
		if !errors.Is(err, tt.wantErr) || v != tt.want {
			t.Errorf("%s: version %d, err %v", tt.name, v, err)
		}
	}
	items, err := e.DB.ListState(ctx, "homekit")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, it := range items {
		got[it.Key] = string(it.Value)
	}
	if len(got) != 2 || got["hap.AccessoryInfo"] != "v3" || got["hap.IdentifierCache"] != "c" {
		t.Errorf("state = %v", got)
	}
	if other, _ := e.DB.ListState(ctx, "matter"); len(other) != 0 {
		t.Errorf("kinds leak: %v", other)
	}

	if err := e.DB.DeleteState(ctx, "homekit", lease, "hap.IdentifierCache"); err != nil {
		t.Fatal(err)
	}
	if err := e.DB.DeleteState(ctx, "homekit", lease, "missing"); err != nil {
		t.Errorf("deleting a missing key: %v", err)
	}

	if _, err := e.DB.IntegrationEndpoint(ctx, "homekit"); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("endpoint before register: %v", err)
	}
	if err := e.DB.RegisterEndpoint(ctx, "homekit", lease, "https://10.0.0.9:8081"); err != nil {
		t.Fatal(err)
	}
	if ep, _ := e.DB.IntegrationEndpoint(ctx, "homekit"); ep != "https://10.0.0.9:8081" {
		t.Errorf("endpoint = %q", ep)
	}

	// The lease dies with its session.
	pid := conn.PgConn().PID()
	conn.Close(ctx)
	e.WaitSessionGone(t, pid)
	if _, err := e.DB.PutState(ctx, "homekit", lease, "a", []byte("x"), 0); !errors.Is(err, db.ErrNotLeader) {
		t.Errorf("write after the session died: %v", err)
	}
	if _, err := e.DB.IntegrationEndpoint(ctx, "homekit"); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("endpoint after the session died: %v", err)
	}
}

func TestCiphertextIsBoundToItsRow(t *testing.T) {
	e := dbtest.Open(t)
	ctx := context.Background()
	conn, _ := e.DB.Dedicated(ctx)
	defer conn.Close(ctx)
	db.TryLock(ctx, conn, e.DB.IntegrationLock("homekit"))
	e.DB.ClaimIntegration(ctx, conn, "homekit", "h", "id", []byte("l"))
	e.DB.PutState(ctx, "homekit", []byte("l"), "a", []byte("secret-a"), 0)
	e.DB.PutState(ctx, "homekit", []byte("l"), "b", []byte("secret-b"), 0)
	if _, err := e.DB.Pool().Exec(ctx, `UPDATE integration_state SET ciphertext = (SELECT ciphertext FROM integration_state WHERE key = 'a'),
		nonce = (SELECT nonce FROM integration_state WHERE key = 'a') WHERE key = 'b'`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.DB.ListState(ctx, "homekit"); !errors.Is(err, secretbox.ErrOpen) {
		t.Errorf("moved ciphertext: err = %v", err)
	}
}

func TestConcurrentConditionalWrites(t *testing.T) {
	e := dbtest.Open(t)
	ctx := context.Background()
	conn, _ := e.DB.Dedicated(ctx)
	defer conn.Close(ctx)
	db.TryLock(ctx, conn, e.DB.IntegrationLock("homekit"))
	lease := []byte("l")
	e.DB.ClaimIntegration(ctx, conn, "homekit", "h", "id", lease)
	e.DB.PutState(ctx, "homekit", lease, "k", []byte("0"), 0)

	var wg sync.WaitGroup
	results := make([]error, 8)
	for i := range results {
		wg.Go(func() {
			_, results[i] = e.DB.PutState(ctx, "homekit", lease, "k", []byte("x"), 1)
		})
	}
	wg.Wait()
	ok := slices.IndexFunc(results, func(err error) bool { return err == nil })
	wins := 0
	for _, err := range results {
		if err == nil {
			wins++
		} else if !errors.Is(err, db.ErrVersionMismatch) {
			t.Errorf("err = %v", err)
		}
	}
	if wins != 1 || ok < 0 {
		t.Errorf("%d conditional writes won, want 1", wins)
	}
}

func TestReencrypt(t *testing.T) {
	e := dbtest.Open(t)
	ctx := context.Background()
	e.DB.TV().SetToken(ctx, "tok", "vigo")
	conn, _ := e.DB.Dedicated(ctx)
	defer conn.Close(ctx)
	db.TryLock(ctx, conn, e.DB.IntegrationLock("homekit"))
	e.DB.ClaimIntegration(ctx, conn, "homekit", "h", "id", []byte("l"))
	e.DB.PutState(ctx, "homekit", []byte("l"), "k", []byte("state"), 0)

	rotated, err := secretbox.AddPrimary(e.Keyring, "k2")
	if err != nil {
		t.Fatal(err)
	}
	d2 := e.Reopen(t, rotated)
	n, err := d2.Reencrypt(ctx)
	if err != nil || n != 2 {
		t.Fatalf("rewrote %d, %v", n, err)
	}
	var ids []string
	rows, _ := d2.Pool().Query(ctx, `SELECT key_id FROM tv_credentials UNION ALL SELECT key_id FROM integration_state`)
	for rows.Next() {
		var id string
		rows.Scan(&id)
		ids = append(ids, id)
	}
	if !slices.Equal(ids, []string{"k2", "k2"}) {
		t.Errorf("key ids after re-encryption = %v", ids)
	}
	if tok, _ := d2.TV().Token(ctx); tok != "tok" {
		t.Errorf("token = %q", tok)
	}
	if items, _ := d2.ListState(ctx, "homekit"); len(items) != 1 || string(items[0].Value) != "state" || items[0].Version != 1 {
		t.Errorf("state = %+v", items)
	}
	if n, err := d2.Reencrypt(ctx); n != 0 || err != nil {
		t.Errorf("second pass rewrote %d, %v", n, err)
	}
	if _, err := e.DB.TV().Token(ctx); err == nil {
		t.Error("old keyring without k2 decrypted the re-encrypted token")
	}
}

// TestVerifyFullTLS runs when VIGO_TEST_DATABASE_TLS_CA names the CA that
// signed the test server's certificate (as CloudNativePG's db-ca does).
func TestVerifyFullTLS(t *testing.T) {
	ca := os.Getenv("VIGO_TEST_DATABASE_TLS_CA")
	if ca == "" {
		t.Skip("VIGO_TEST_DATABASE_TLS_CA is not set")
	}
	e := dbtest.Open(t)
	ctx := context.Background()
	o := e.Options
	o.CAFile = ca
	d, err := db.Open(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var ssl bool
	if err := d.Pool().QueryRow(ctx, `SELECT ssl FROM pg_stat_ssl WHERE pid = pg_backend_pid()`).Scan(&ssl); err != nil || !ssl {
		t.Errorf("connection not encrypted: %v %v", ssl, err)
	}

	other, err := pkitest.NewCA("other")
	if err != nil {
		t.Fatal(err)
	}
	otherFile := filepath.Join(t.TempDir(), "ca.crt")
	os.WriteFile(otherFile, other.PEM, 0o600)
	o.CAFile = otherFile
	if d, err := db.Open(ctx, o); err == nil {
		d.Close()
		t.Error("connected with a CA that did not sign the server certificate")
	}
}
