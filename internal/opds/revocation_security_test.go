package opds

import (
	"discodrive/internal/db"
	"net/http/httptest"
	"testing"
)

func TestCredentialsRevokedOnPasswordChange(t *testing.T) {
	h, ctx := setupOPDS(t)
	user, err := h.q.GetUserByEmail(ctx, testEmail)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := h.q.GetEbookSettings(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	check := func(want bool) {
		t.Helper()
		r := httptest.NewRequest("GET", "/", nil)
		r.SetBasicAuth(testEmail, testPassword)
		if _, ok := h.authenticate(r); ok != want {
			t.Fatalf("password accepted=%v, want %v", ok, want)
		}
		r = httptest.NewRequest("GET", "/?apiKey="+testAPIKey, nil)
		if _, ok := h.authenticate(r); ok != want {
			t.Fatalf("API key accepted=%v, want %v", ok, want)
		}
	}
	check(true)
	updated, err := h.q.UpdatePassword(ctx, db.UpdatePasswordParams{ID: user.ID, PasswordHash: "changed", PreviousPasswordHash: user.PasswordHash})
	if err != nil {
		t.Fatal(err)
	}
	check(false)
	// A stale in-flight request cannot create credentials for a new generation.
	if err := h.q.SetEbookCredentials(ctx, db.SetEbookCredentialsParams{UserID: user.ID, PasswordCipher: settings.PasswordCipher, ApiKey: settings.ApiKey, TokenVersion: user.TokenVersion}); err != nil {
		t.Fatal(err)
	}
	check(false)
	// Explicitly issuing credentials from the new session restores the integration.
	if err := h.q.SetEbookCredentials(ctx, db.SetEbookCredentialsParams{UserID: user.ID, PasswordCipher: settings.PasswordCipher, ApiKey: settings.ApiKey, TokenVersion: updated.TokenVersion}); err != nil {
		t.Fatal(err)
	}
	check(true)
}
