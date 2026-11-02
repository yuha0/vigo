// Package secretbox encrypts values before they reach the database:
// AES-256-GCM with a random 96-bit nonce, additional authenticated data
// binding each ciphertext to its table, column and row key, and a keyring
// so keys can be rotated without losing old values.
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
)

const keySize = 32

// ErrOpen means a value failed authentication: the wrong key, the wrong
// row or a tampered ciphertext. The causes are deliberately not
// distinguished.
var ErrOpen = errors.New("secretbox: message authentication failed")

// Sealed is an encrypted value as stored.
type Sealed struct {
	KeyID      string
	Nonce      []byte
	Ciphertext []byte
}

// Keyring holds the encryption keys. Seal always uses the primary key; Open
// uses whichever key a value names.
type Keyring struct {
	primary string
	aeads   map[string]cipher.AEAD
}

type keyringFile struct {
	Primary string            `json:"primary"`
	Keys    map[string]string `json:"keys"`
}

var keyIDPattern = regexp.MustCompile(`^[-._a-zA-Z0-9]{1,64}$`)

// LoadKeyring reads a keyring file:
//
//	{"primary":"k1","keys":{"k1":"<base64 of 32 bytes>"}}
func LoadKeyring(path string) (*Keyring, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("secretbox: %w", err)
	}
	return ParseKeyring(b)
}

func ParseKeyring(b []byte) (*Keyring, error) {
	var f keyringFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("secretbox: parse keyring: %w", err)
	}
	if _, ok := f.Keys[f.Primary]; !ok {
		return nil, fmt.Errorf("secretbox: primary key %q is not in the keyring", f.Primary)
	}
	k := &Keyring{primary: f.Primary, aeads: map[string]cipher.AEAD{}}
	for id, enc := range f.Keys {
		if !keyIDPattern.MatchString(id) {
			return nil, fmt.Errorf("secretbox: invalid key id %q", id)
		}
		raw, err := base64.StdEncoding.DecodeString(enc)
		if err != nil || len(raw) != keySize {
			return nil, fmt.Errorf("secretbox: key %q must be base64 of %d bytes", id, keySize)
		}
		block, err := aes.NewCipher(raw)
		if err != nil {
			return nil, fmt.Errorf("secretbox: key %q: %w", id, err)
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, fmt.Errorf("secretbox: key %q: %w", id, err)
		}
		k.aeads[id] = aead
	}
	return k, nil
}

// Primary is the id of the key new values are sealed with.
func (k *Keyring) Primary() string { return k.primary }

// KeyIDs lists the ids in the keyring, sorted.
func (k *Keyring) KeyIDs() []string {
	ids := make([]string, 0, len(k.aeads))
	for id := range k.aeads {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// AAD binds a value to where it is stored. Each part is length-prefixed so
// no two different locations encode the same.
func AAD(table, column, row string) []byte {
	var b []byte
	b = append(b, "vigo.secretbox.v1"...)
	for _, p := range []string{table, column, row} {
		b = binary.BigEndian.AppendUint32(b, uint32(len(p)))
		b = append(b, p...)
	}
	return b
}

// Seal encrypts plaintext with the primary key.
func (k *Keyring) Seal(plaintext, aad []byte) (Sealed, error) {
	aead := k.aeads[k.primary]
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return Sealed{}, fmt.Errorf("secretbox: nonce: %w", err)
	}
	return Sealed{KeyID: k.primary, Nonce: nonce, Ciphertext: aead.Seal(nil, nonce, plaintext, aad)}, nil
}

// Open decrypts s, which must have been sealed with the same aad.
func (k *Keyring) Open(s Sealed, aad []byte) ([]byte, error) {
	aead, ok := k.aeads[s.KeyID]
	if !ok {
		return nil, fmt.Errorf("secretbox: key %q is not in the keyring", s.KeyID)
	}
	if len(s.Nonce) != aead.NonceSize() {
		return nil, ErrOpen
	}
	pt, err := aead.Open(nil, s.Nonce, s.Ciphertext, aad)
	if err != nil {
		return nil, ErrOpen
	}
	return pt, nil
}

// NewKey returns a random key, base64-encoded as the keyring file expects.
func NewKey() (string, error) {
	raw := make([]byte, keySize)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}

// AddPrimary returns keyring file content with a new random key added under
// id and made primary. existing may be nil to start a new keyring.
func AddPrimary(existing []byte, id string) ([]byte, error) {
	if !keyIDPattern.MatchString(id) {
		return nil, fmt.Errorf("secretbox: invalid key id %q", id)
	}
	f := keyringFile{Keys: map[string]string{}}
	if existing != nil {
		if _, err := ParseKeyring(existing); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(existing, &f); err != nil {
			return nil, err
		}
	}
	if _, ok := f.Keys[id]; ok {
		return nil, fmt.Errorf("secretbox: key %q already exists", id)
	}
	key, err := NewKey()
	if err != nil {
		return nil, err
	}
	f.Keys[id] = key
	f.Primary = id
	return json.MarshalIndent(f, "", "  ")
}
