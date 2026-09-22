package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/jackc/pgx/v5/pgxpool"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"discodrive/internal/db"
)

func TestNewWebAuthn(t *testing.T) {
	wa, err := NewWebAuthn("")
	if err != nil || wa != nil {
		t.Fatalf("empty domain must yield (nil,nil), got (%v,%v)", wa, err)
	}
	for _, in := range []string{"disco.example.com", "localhost:8443", "https://localhost:8443"} {
		wa, err = NewWebAuthn(in)
		if err != nil || wa == nil {
			t.Fatalf("NewWebAuthn(%q) must yield an instance, got (%v,%v)", in, wa, err)
		}
	}
}

func TestWebAuthnSessionTokenRoundTrip(t *testing.T) {
	iss := NewTokenIssuer("secret", time.Hour)
	tok, err := iss.IssueWebAuthnSession("user-1", "ZGF0YQ==", 0)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	subj, data, err := iss.ParseWebAuthnSession(tok)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if subj != "user-1" || data != "ZGF0YQ==" {
		t.Fatalf("round-trip mismatch: subj=%q data=%q", subj, data)
	}
	if _, _, err := iss.ParseWebAuthnSession("not-a-token"); err == nil {
		t.Fatal("garbage token must not parse")
	}
}

func TestBeginWebAuthnLogin(t *testing.T) {
	svc := &Service{issuer: NewTokenIssuer("secret", time.Hour)}
	if _, _, err := svc.BeginWebAuthnLogin(context.Background()); err != ErrWebAuthnNotConfigured {
		t.Fatalf("nil relying party must refuse, got %v", err)
	}
	wa, _ := NewWebAuthn("disco.example.com")
	svc.SetWebAuthn(wa)
	opts, tok, err := svc.BeginWebAuthnLogin(context.Background())
	if err != nil {
		t.Fatalf("BeginWebAuthnLogin: %v", err)
	}
	if !bytes.Contains(opts, []byte("challenge")) {
		t.Fatalf("login options must contain a challenge: %s", opts)
	}
	if _, data, err := svc.issuer.ParseWebAuthnSession(tok); err != nil || data == "" {
		t.Fatalf("session token must carry SessionData: err=%v data=%q", err, data)
	}
}

func TestWebAuthnRegisterBeginAndCRUD(t *testing.T) {
	ctx := context.Background()
	pgC, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("kf"), tcpostgres.WithUsername("kf"), tcpostgres.WithPassword("kf"),
		tcpostgres.BasicWaitStrategies())
	if err != nil {
		t.Skipf("need Docker: %v", err)
	}
	t.Cleanup(func() { _ = pgC.Terminate(ctx) })

	dsn, _ := pgC.ConnectionString(ctx, "sslmode=disable")
	if err := db.MigrateUp(dsn); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool: %v", err)
	}
	t.Cleanup(pool.Close)

	svc := NewService(pool, NewTokenIssuer("secret", time.Hour), nil)
	q := db.New(pool)
	tenant, _ := q.CreateTenant(ctx, "t")
	passwordHash, _ := HashPassword("test-password")
	u, err := q.CreateUser(ctx, db.CreateUserParams{TenantID: tenant.ID, Email: "wa@test.local", PasswordHash: passwordHash, Role: "user"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	userID := db.UUIDString(u.ID)

	// Without a configured relying party, registration is refused.
	if _, _, err := svc.BeginWebAuthnRegistration(ctx, userID); err != ErrWebAuthnNotConfigured {
		t.Fatalf("expected ErrWebAuthnNotConfigured, got %v", err)
	}

	// With a relying party, begin produces options (carrying a challenge) + a parseable token.
	wa, _ := NewWebAuthn("disco.example.com")
	svc.SetWebAuthn(wa)
	approval, err := svc.ApprovePasskeyWithPassword(ctx, userID, "test-password", "", "register")
	if err != nil {
		t.Fatal(err)
	}
	opts, sessionToken, err := svc.BeginWebAuthnRegistration(ctx, userID, approval)
	if err != nil {
		t.Fatalf("BeginWebAuthnRegistration: %v", err)
	}
	if !bytes.Contains(opts, []byte("challenge")) {
		t.Fatalf("options must contain a challenge: %s", opts)
	}
	if _, data, err := svc.issuer.ParseWebAuthnSession(sessionToken); err != nil || data == "" {
		t.Fatalf("session token must carry SessionData: err=%v data=%q", err, data)
	}

	// CRUD: the full credential is stored as JSON and round-trips via loadWAUser.
	cred := webauthn.Credential{ID: []byte{1, 2, 3, 4}, PublicKey: []byte{5, 6, 7, 8}}
	blob, _ := json.Marshal(cred)
	if _, err := q.InsertWebAuthnCredential(ctx, db.InsertWebAuthnCredentialParams{
		UserID: u.ID, CredentialID: cred.ID, Credential: blob, Name: "YubiKey",
	}); err != nil {
		t.Fatalf("InsertWebAuthnCredential: %v", err)
	}
	rows, _ := q.ListWebAuthnCredentials(ctx, u.ID)
	if len(rows) != 1 || rows[0].Name != "YubiKey" {
		t.Fatalf("expected 1 credential named YubiKey, got %+v", rows)
	}
	wu, err := svc.loadWAUser(ctx, u.ID)
	if err != nil || len(wu.creds) != 1 || !bytes.Equal(wu.creds[0].ID, cred.ID) {
		t.Fatalf("loadWAUser must decode the stored credential: %+v %v", wu, err)
	}

	if err := q.RenameWebAuthnCredential(ctx, db.RenameWebAuthnCredentialParams{ID: rows[0].ID, UserID: u.ID, Name: "Phone"}); err != nil {
		t.Fatalf("rename: %v", err)
	}
	rows, _ = q.ListWebAuthnCredentials(ctx, u.ID)
	if rows[0].Name != "Phone" {
		t.Fatalf("rename did not take: %q", rows[0].Name)
	}
	if err := q.DeleteWebAuthnCredential(ctx, db.DeleteWebAuthnCredentialParams{ID: rows[0].ID, UserID: u.ID}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	rows, _ = q.ListWebAuthnCredentials(ctx, u.ID)
	if len(rows) != 0 {
		t.Fatalf("expected no credentials after delete, got %d", len(rows))
	}
}
