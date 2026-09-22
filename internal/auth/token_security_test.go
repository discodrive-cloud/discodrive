package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/db"
)

func TestWebAuthnCeremonyCannotAuthorizeAPI(t *testing.T) {
	iss := NewTokenIssuer("review-only-secret", time.Hour)
	uid, _ := db.ParseUUID(validUUID)
	svc := &Service{issuer: iss, lookupUser: func(context.Context, pgtype.UUID) (db.User, error) {
		return db.User{ID: uid, Role: "user", TokenVersion: 0, SessionTtlMinutes: 60}, nil
	}}
	tok, err := iss.IssueWebAuthnSession(validUUID, "ZGF0YQ==", 0)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/files", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	svc.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || rec.Header().Get("X-Token") != "" {
		t.Fatalf("ceremony authorized API: status=%d", rec.Code)
	}
}

func TestLegacyWebAuthnTokensRejected(t *testing.T) {
	iss := NewTokenIssuer("test-secret", time.Hour)
	legacy, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"sub": validUUID, "was": "ZGF0YQ==", "exp": time.Now().Add(time.Minute).Unix()}).SignedString([]byte("test-secret"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := iss.Parse(legacy); err == nil {
		t.Fatal("legacy ceremony accepted as access token")
	}
	if _, _, err := iss.ParseWebAuthnSession(legacy); err == nil {
		t.Fatal("untyped ceremony accepted")
	}
	access, err := iss.Issue(validUUID, "tenant", "user", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := iss.ParseWebAuthnSession(access); err == nil {
		t.Fatal("access token accepted as ceremony")
	}
	for _, subject := range []string{"", validUUID} {
		tok, err := iss.IssueWebAuthnSession(subject, "ZGF0YQ==", 0)
		if err != nil {
			t.Fatal(err)
		}
		got, _, err := iss.ParseWebAuthnSession(tok)
		if err != nil || got != subject {
			t.Fatalf("ceremony round trip: %v", err)
		}
		if _, err := iss.Parse(tok); err == nil {
			t.Fatal("typed ceremony accepted as access token")
		}
	}
}
