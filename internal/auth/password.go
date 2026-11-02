package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

const (
	// MinPasswordLength is in characters; maxPasswordBytes bounds the work
	// a single request can cause.
	MinPasswordLength = 12
	maxPasswordBytes  = 1024
)

// Params are argon2id parameters.
type Params struct {
	MemoryKiB uint32
	Time      uint32
	Threads   uint8
	SaltLen   int
	KeyLen    uint32
}

// DefaultParams: 64 MiB, 3 passes, 4 lanes, 16-byte salt, 32-byte key.
var DefaultParams = Params{MemoryKiB: 64 * 1024, Time: 3, Threads: 4, SaltLen: 16, KeyLen: 32}

var errBadHash = errors.New("auth: malformed password hash")

func checkPassword(p string) error {
	switch {
	case len(p) > maxPasswordBytes:
		return fmt.Errorf("the password is longer than %d bytes", maxPasswordBytes)
	case !utf8.ValidString(p):
		return errors.New("the password is not valid UTF-8")
	case utf8.RuneCountInString(p) < MinPasswordLength:
		return fmt.Errorf("the password must be at least %d characters", MinPasswordLength)
	}
	return nil
}

var b64 = base64.RawStdEncoding

// hashPassword returns a PHC string:
// $argon2id$v=19$m=65536,t=3,p=4$<salt>$<hash>
func hashPassword(password string, p Params) (string, error) {
	salt := make([]byte, p.SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, p.Time, p.MemoryKiB, p.Threads, p.KeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.MemoryKiB, p.Time, p.Threads, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// verifyPassword checks password against a PHC string in constant time,
// using the parameters stored in it.
func verifyPassword(password, phc string) (bool, error) {
	parts := strings.Split(phc, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errBadHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, errBadHash
	}
	var p Params
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.MemoryKiB, &p.Time, &p.Threads); err != nil {
		return false, errBadHash
	}
	salt, err := b64.DecodeString(parts[4])
	if err != nil {
		return false, errBadHash
	}
	want, err := b64.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return false, errBadHash
	}
	got := argon2.IDKey([]byte(password), salt, p.Time, p.MemoryKiB, p.Threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}
