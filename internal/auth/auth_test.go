package auth

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/db"
)

// buildLoginService wires a Service with stubbed seams: a user fetched by email and a
// configurable set of available second factors. No real DB.
func buildLoginService(t *testing.T, hash string, factors []string) *Service {
	t.Helper()
	uid, _ := db.ParseUUID("11111111-1111-1111-1111-111111111111")
	tid, _ := db.ParseUUID("22222222-2222-2222-2222-222222222222")
	u := db.User{ID: uid, TenantID: tid, Role: "user", Email: "a@test.local", PasswordHash: hash}
	return &Service{
		createSession:    func(context.Context, db.User) (string, error) { return "test-session", nil },
		issuer:           NewTokenIssuer("secret", time.Hour),
		getUserByEmail:   func(context.Context, string) (db.User, error) { return u, nil },
		availableFactors: func(context.Context, pgtype.UUID) ([]string, error) { return factors, nil },
	}
}

func TestLogin_NoMFA_ReturnsSessionToken(t *testing.T) {
	hash, _ := HashPassword("correct horse")
	svc := buildLoginService(t, hash, nil)

	res, err := svc.Login(context.Background(), "a@test.local", "correct horse")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if res.Token == "" || res.MFAToken != "" {
		t.Fatalf("expected full session token, got %+v", res)
	}
	claims, _ := svc.issuer.Parse(res.Token)
	if claims.Pur != "" {
		t.Fatalf("session token must have empty purpose, got %q", claims.Pur)
	}
}

func TestLogin_WithMFA_ReturnsMFAToken(t *testing.T) {
	hash, _ := HashPassword("correct horse")
	svc := buildLoginService(t, hash, []string{"totp"})

	res, err := svc.Login(context.Background(), "a@test.local", "correct horse")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if res.MFAToken == "" || res.Token != "" {
		t.Fatalf("expected MFA-pending token, got %+v", res)
	}
	if len(res.Methods) != 1 || res.Methods[0] != "totp" {
		t.Fatalf("methods=%v, want [totp]", res.Methods)
	}
	claims, _ := svc.issuer.Parse(res.MFAToken)
	if claims.Pur != "mfa" {
		t.Fatalf("MFA token purpose=%q, want mfa", claims.Pur)
	}
}

func TestLogin_WrongPassword(t *testing.T) {
	hash, _ := HashPassword("correct horse")
	svc := buildLoginService(t, hash, nil)
	if _, err := svc.Login(context.Background(), "a@test.local", "wrong"); err != ErrInvalidCreds {
		t.Fatalf("err=%v, want ErrInvalidCreds", err)
	}
}
