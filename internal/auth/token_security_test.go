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

func TestLogoutTokenIgnoresOnlyExpiration(t *testing.T) {
	iss := NewTokenIssuer("logout-secret", time.Hour)
	for _, tc := range []struct {
		name    string
		mutate  func(*Claims)
		key     string
		allowed bool
	}{
		{"expired browser", func(c *Claims) {}, "logout-secret", true},
		{"wrong signature", func(c *Claims) {}, "other-secret", false},
		{"future validity", func(c *Claims) { c.NotBefore = jwt.NewNumericDate(time.Now().Add(time.Hour)) }, "logout-secret", false},
		{"stream", func(c *Claims) { c.Pur = "stream-v2" }, "logout-secret", false},
		{"mfa", func(c *Claims) { c.Pur = "mfa" }, "logout-secret", false},
		{"ceremony", func(c *Claims) { c.LegacyWebAuthn = "data" }, "logout-secret", false},
		{"device", func(c *Claims) { c.DeviceID = validUUID }, "logout-secret", false},
		{"missing session", func(c *Claims) { c.SessionID = "" }, "logout-secret", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := Claims{SessionID: "session", RegisteredClaims: jwt.RegisteredClaims{
				Subject: validUUID, ExpiresAt: jwt.NewNumericDate(time.Now().Add(-time.Minute)),
			}}
			tc.mutate(&c)
			token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString([]byte(tc.key))
			if err != nil {
				t.Fatal(err)
			}
			_, err = iss.parseLogout(token)
			if (err == nil) != tc.allowed {
				t.Fatalf("allowed=%v error=%v", tc.allowed, err)
			}
		})
	}
}

func TestWebAuthnCeremonyCannotAuthorizeAPI(t *testing.T) {
	iss := NewTokenIssuer("review-only-secret", time.Hour)
	uid, _ := db.ParseUUID(validUUID)
	svc := &Service{issuer: iss, sessionActive: func(_ context.Context, c *Claims) bool { return c.SessionID == "test-session" }, lookupUser: func(context.Context, pgtype.UUID) (db.User, error) {
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
	access, err := iss.Issue(validUUID, "tenant", "user", 0, "", "test-session")
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
