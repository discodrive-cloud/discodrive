package subsonic

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"net/http"
	"strings"

	"discodrive/internal/db"

	"github.com/jackc/pgx/v5/pgtype"
)

// authenticate resolves a userID from the request parameters.
// Auth order: apiKey → token (u+t+s) → password (u+p).
// Returns (userID, true) on success, ("", false) on any failure.
// Never logs the decrypted password.
func (h *Handler) authenticate(r *http.Request) (userID string, ok bool) {
	ctx := context.Background()

	// --- apiKey auth ---
	if apiKey := r.FormValue("apiKey"); apiKey != "" {
		settings, err := h.q.GetMusicSettingsByApiKey(ctx, pgtype.Text{String: apiKey, Valid: true})
		if err != nil || !settings.Enabled {
			return "", false
		}
		return db.UUIDString(settings.UserID), true
	}

	// --- token or password auth: both require 'u' (email) ---
	email := r.FormValue("u")
	if email == "" {
		return "", false
	}

	user, err := h.q.GetUserByEmail(ctx, email)
	if err != nil {
		return "", false
	}

	settings, err := h.q.GetMusicSettings(ctx, user.ID)
	if err != nil || !settings.Enabled || !settings.PasswordCipher.Valid || settings.TokenVersion != user.TokenVersion || user.MustChangePassword {
		return "", false
	}

	plain, err := h.cipher.Decrypt(settings.PasswordCipher.String)
	if err != nil {
		return "", false
	}

	t := r.FormValue("t")
	s := r.FormValue("s")
	p := r.FormValue("p")

	switch {
	case t != "" && s != "":
		// Token auth: md5(plain + salt)
		sum := md5.Sum([]byte(plain + s))
		expected := hex.EncodeToString(sum[:])
		if strings.EqualFold(expected, t) {
			return db.UUIDString(user.ID), true
		}
		return "", false

	case p != "":
		// Password auth: candidate is hex-decoded if prefixed with "enc:", else literal.
		candidate := p
		if strings.HasPrefix(p, "enc:") {
			decoded, err := hex.DecodeString(p[4:])
			if err != nil {
				return "", false
			}
			candidate = string(decoded)
		}
		if candidate == plain {
			return db.UUIDString(user.ID), true
		}
		return "", false
	}

	return "", false
}
