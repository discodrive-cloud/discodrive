package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"runtime"
	"strings"

	"golang.org/x/crypto/argon2"
)

// argonParams holds argon2id tuning parameters (OWASP-recommended defaults).
type argonParams struct {
	memory  uint32
	time    uint32
	threads uint8
	keyLen  uint32
	saltLen uint32
}

var defaultArgon = argonParams{memory: 64 * 1024, time: 1, threads: 4, keyLen: 32, saltLen: 16}

// ErrInvalidHash is returned when the stored hash has an unexpected format.
var ErrInvalidHash = errors.New("invalid password hash format")

// HashPassword returns an argon2id hash in the format
// $argon2id$v=19$m=...,t=...,p=...$salt$hash (raw base64).
func HashPassword(password string) (string, error) {
	p := defaultArgon
	salt := make([]byte, p.saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	hash := argonKey([]byte(password), salt, p.time, p.memory, p.threads, p.keyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.memory, p.time, p.threads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash),
	), nil
}

// verifyPassword is VerifyPassword behind a seam, so tests can count Argon2 checks.
var verifyPassword = VerifyPassword

// dummyHash is checked when there is no real hash to check against (unknown email,
// no app passwords), so a miss costs the same Argon2 run as a wrong password and
// response time does not tell whether an account exists. It uses the current
// parameters and random bytes: no password matches it, and building it runs no Argon2.
var dummyHash = func() string {
	p := defaultArgon
	salt, key := make([]byte, p.saltLen), make([]byte, p.keyLen)
	_, _ = rand.Read(salt)
	_, _ = rand.Read(key)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.memory, p.time, p.threads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key))
}()

// burnPasswordCheck spends one Argon2 verification (same path: GC first, then the
// hash) and discards the result.
func burnPasswordCheck(password string) { _, _ = verifyPassword(password, dummyHash) }

// VerifyPassword checks a password against a stored hash (constant-time comparison).
func VerifyPassword(password, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, ErrInvalidHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, ErrInvalidHash
	}
	var p argonParams
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.memory, &p.time, &p.threads); err != nil {
		return false, ErrInvalidHash
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, ErrInvalidHash
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, ErrInvalidHash
	}
	got := argonKey([]byte(password), salt, p.time, p.memory, p.threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// argonKey runs Argon2id after a garbage collection. Its work area (m KiB, 64 MiB by
// default) is one allocation, and the previous check left the GC target near twice
// that, so up to ~100 MiB of garbage may be sitting uncollected: allocating on top of
// it OOM-killed the server on 128 MB machines. Checks are rare (logins, and a DAV
// client once per cache TTL), so a collection each costs little.
func argonKey(password, salt []byte, time, memory uint32, threads uint8, keyLen uint32) []byte {
	runtime.GC()
	return argon2.IDKey(password, salt, time, memory, threads, keyLen)
}
