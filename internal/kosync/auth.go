package kosync

import (
	"crypto/md5"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"

	"discodrive/internal/db"
)

// authUser parses x-auth-user (email) and x-auth-key (md5 hex of ebook password).
// Returns (userID string, email string, ok bool).
// Returns ("", "", false) on any failure. Never logs secrets.
func (h *Handler) authUser(r *http.Request) (userID string, email string, ok bool) {
	ctx := r.Context()

	email = r.Header.Get("x-auth-user")
	key := r.Header.Get("x-auth-key")

	if email == "" || key == "" {
		return "", "", false
	}

	user, err := h.q.GetUserByEmail(ctx, email)
	if err != nil {
		return "", "", false
	}

	settings, err := h.q.GetEbookSettings(ctx, user.ID)
	if err != nil || !settings.Enabled || !settings.PasswordCipher.Valid || settings.TokenVersion != user.TokenVersion || user.MustChangePassword {
		return "", "", false
	}

	plain, err := h.cipher.Decrypt(settings.PasswordCipher.String)
	if err != nil {
		return "", "", false
	}

	// Compute expected md5 hex of the plaintext password.
	sum := md5.Sum([]byte(plain))
	expected := hex.EncodeToString(sum[:])

	// Constant-time comparison to prevent timing attacks.
	if subtle.ConstantTimeCompare([]byte(strings.ToLower(expected)), []byte(strings.ToLower(key))) != 1 {
		return "", "", false
	}

	return db.UUIDString(user.ID), email, true
}
