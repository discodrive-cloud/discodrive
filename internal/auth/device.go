package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"math/big"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/db"
)

// userCodeAlphabet excludes visually ambiguous characters: 0/O/1/I/L.
const userCodeAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

// ErrDeviceToken is returned when a device token does not match any live device (including revoked ones).
var ErrDeviceToken = errors.New("device token invalid")

// newUserCode returns an 8-character code in ABCD-EFGH format (human-readable).
func newUserCode() string {
	b := make([]byte, 8)
	for i := range b {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(userCodeAlphabet))))
		b[i] = userCodeAlphabet[n.Int64()]
	}
	return string(b[:4]) + "-" + string(b[4:])
}

// newDeviceCode returns a high-entropy polling secret (the daemon stores it; the server stores its hash).
func newDeviceCode() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// newDeviceToken returns a long-lived opaque device token (refresh), prefixed with kfd_.
func newDeviceToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "kfd_" + base64.RawURLEncoding.EncodeToString(b), nil
}

// tokenHash returns a sha256 hex digest. Tokens are high-entropy, so no salt is needed; lookup is O(1).
func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Exported wrappers — required by the API layer (internal/api/pairing.go, Task 5).
func NewUserCode() string             { return newUserCode() }
func NewDeviceCode() (string, error)  { return newDeviceCode() }
func NewDeviceToken() (string, error) { return newDeviceToken() }
func TokenHash(t string) string       { return tokenHash(t) }

// DeviceTokenExchange exchanges a device token for a session JWT (refresh → access).
// If the device has been revoked (no row with that token_hash), returns ErrDeviceToken.
func (s *Service) DeviceTokenExchange(ctx context.Context, deviceToken string) (string, error) {
	dev, err := s.q.GetDeviceByTokenHash(ctx, pgtype.Text{String: tokenHash(deviceToken), Valid: true})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrDeviceToken
	}
	if err != nil {
		return "", err
	}
	u, err := s.q.GetUserByID(ctx, dev.UserID)
	if err != nil {
		return "", err
	}
	// Never upgrade an old device credential to the current password generation.
	if dev.TokenVersion != u.TokenVersion || u.MustChangePassword {
		return "", ErrDeviceToken
	}
	_ = s.q.TouchDevice(ctx, dev.ID)
	return s.issueForDevice(u, db.UUIDString(dev.ID))
}
