package opds

import (
	"crypto/subtle"
	"net/http"

	"discodrive/internal/db"

	"github.com/jackc/pgx/v5/pgtype"
)

// authenticate resolves a userID from the request.
// Auth order: ?apiKey= query param → HTTP Basic (email + ebook password).
// Returns (userID, true) on success, ("", false) on any failure.
// Never logs the decrypted password or any secret.
func (h *Handler) authenticate(r *http.Request) (userID string, ok bool) {
	ctx := r.Context()

	// --- apiKey auth ---
	if apiKey := r.FormValue("apiKey"); apiKey != "" {
		settings, err := h.q.GetEbookSettingsByApiKey(ctx, pgtype.Text{String: apiKey, Valid: true})
		if err != nil || !settings.Enabled {
			return "", false
		}
		return db.UUIDString(settings.UserID), true
	}

	// --- Basic auth: username=email, password=ebook password ---
	email, password, ok := r.BasicAuth()
	if !ok || email == "" {
		return "", false
	}

	user, err := h.q.GetUserByEmail(ctx, email)
	if err != nil {
		return "", false
	}

	settings, err := h.q.GetEbookSettings(ctx, user.ID)
	if err != nil || !settings.Enabled || !settings.PasswordCipher.Valid || settings.TokenVersion != user.TokenVersion || user.MustChangePassword {
		return "", false
	}

	plain, err := h.cipher.Decrypt(settings.PasswordCipher.String)
	if err != nil {
		return "", false
	}

	// Constant-time comparison to prevent timing attacks.
	if subtle.ConstantTimeCompare([]byte(plain), []byte(password)) != 1 {
		return "", false
	}

	return db.UUIDString(user.ID), true
}
