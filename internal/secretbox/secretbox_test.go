package secretbox

import (
	"bytes"
	"errors"
	"testing"
)

func mustKeyring(t *testing.T, b []byte) *Keyring {
	t.Helper()
	k, err := ParseKeyring(b)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestSealOpen(t *testing.T) {
	file, err := AddPrimary(nil, "k1")
	if err != nil {
		t.Fatal(err)
	}
	k := mustKeyring(t, file)
	aad := AAD("tv_credentials", "token", "1")
	s, err := k.Seal([]byte("secret-token"), aad)
	if err != nil {
		t.Fatal(err)
	}
	if s.KeyID != "k1" || len(s.Nonce) != 12 || bytes.Contains(s.Ciphertext, []byte("secret")) {
		t.Fatalf("sealed = %+v", s)
	}
	pt, err := k.Open(s, aad)
	if err != nil || string(pt) != "secret-token" {
		t.Fatalf("Open = %q, %v", pt, err)
	}
	s2, _ := k.Seal([]byte("secret-token"), aad)
	if bytes.Equal(s.Nonce, s2.Nonce) || bytes.Equal(s.Ciphertext, s2.Ciphertext) {
		t.Error("nonce reused")
	}
}

func TestOpenRejects(t *testing.T) {
	k := mustKeyring(t, must(AddPrimary(nil, "k1")))
	other := mustKeyring(t, must(AddPrimary(nil, "k1")))
	aad := AAD("integration_state", "value", "homekit/hap.AccessoryInfo")
	s, err := k.Seal([]byte("pairing keys"), aad)
	if err != nil {
		t.Fatal(err)
	}
	flip := func(b []byte, i int) []byte {
		c := bytes.Clone(b)
		c[i] ^= 1
		return c
	}
	tests := []struct {
		name string
		k    *Keyring
		s    Sealed
		aad  []byte
	}{
		{"other row", k, s, AAD("integration_state", "value", "homekit/hap.other")},
		{"other column", k, s, AAD("integration_state", "key", "homekit/hap.AccessoryInfo")},
		{"other table", k, s, AAD("tv_credentials", "value", "homekit/hap.AccessoryInfo")},
		{"tampered ciphertext", k, Sealed{s.KeyID, s.Nonce, flip(s.Ciphertext, 0)}, aad},
		{"tampered tag", k, Sealed{s.KeyID, s.Nonce, flip(s.Ciphertext, len(s.Ciphertext)-1)}, aad},
		{"tampered nonce", k, Sealed{s.KeyID, flip(s.Nonce, 3), s.Ciphertext}, aad},
		{"short nonce", k, Sealed{s.KeyID, s.Nonce[:8], s.Ciphertext}, aad},
		{"wrong key material", other, s, aad},
	}
	for _, tt := range tests {
		if _, err := tt.k.Open(tt.s, tt.aad); !errors.Is(err, ErrOpen) {
			t.Errorf("%s: err = %v, want ErrOpen", tt.name, err)
		}
	}
	if _, err := k.Open(Sealed{"k9", s.Nonce, s.Ciphertext}, aad); err == nil {
		t.Error("unknown key id accepted")
	}
}

func TestAADIsUnambiguous(t *testing.T) {
	if bytes.Equal(AAD("ab", "c", "d"), AAD("a", "bc", "d")) {
		t.Error("AAD parts are not delimited")
	}
}

func TestRotation(t *testing.T) {
	v1 := must(AddPrimary(nil, "k1"))
	k1 := mustKeyring(t, v1)
	aad := AAD("tv_credentials", "token", "1")
	old, _ := k1.Seal([]byte("token"), aad)

	v2 := must(AddPrimary(v1, "k2"))
	k2 := mustKeyring(t, v2)
	if k2.Primary() != "k2" || len(k2.KeyIDs()) != 2 {
		t.Fatalf("keyring = %v primary %s", k2.KeyIDs(), k2.Primary())
	}
	pt, err := k2.Open(old, aad)
	if err != nil || string(pt) != "token" {
		t.Fatalf("old value after rotation: %q, %v", pt, err)
	}
	fresh, _ := k2.Seal(pt, aad)
	if fresh.KeyID != "k2" {
		t.Errorf("new writes use %s", fresh.KeyID)
	}
	if _, err := k1.Open(fresh, aad); err == nil {
		t.Error("old keyring opened a value sealed with the new key")
	}
	if _, err := AddPrimary(v2, "k1"); err == nil {
		t.Error("duplicate key id accepted")
	}
}

func TestParseKeyringErrors(t *testing.T) {
	for name, in := range map[string]string{
		"not json":        "nope",
		"missing primary": `{"primary":"k2","keys":{"k1":"` + must2(NewKey()) + `"}}`,
		"short key":       `{"primary":"k1","keys":{"k1":"c2hvcnQ="}}`,
		"bad id":          `{"primary":"a b","keys":{"a b":"` + must2(NewKey()) + `"}}`,
	} {
		if _, err := ParseKeyring([]byte(in)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func must(b []byte, err error) []byte {
	if err != nil {
		panic(err)
	}
	return b
}

func must2(s string, err error) string {
	if err != nil {
		panic(err)
	}
	return s
}
