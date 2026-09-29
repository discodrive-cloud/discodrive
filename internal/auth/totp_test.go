package auth

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pquerna/otp/totp"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"discodrive/internal/db"
	"discodrive/internal/secret"
)

// TestTOTPLifecycle exercises the full 2FA flow against a real DB: setup → confirm with a
// generated code → login now requires a second factor → complete with TOTP → complete with a
// one-time backup code (and reuse fails) → disable requires password + a current code.
func TestTOTPLifecycle(t *testing.T) {
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

	cipher, err := secret.New("0123456789abcdef0123456789abcdef") // 32 bytes
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	issuer := NewTokenIssuer("secret", time.Hour)
	svc := NewService(pool, issuer, cipher)
	// Each code is single-use per 30 s step: move the clock one step before each use.
	now := time.Now()
	totpNow = func() time.Time { return now }
	t.Cleanup(func() { totpNow = time.Now })
	nextCode := func(secret string) string {
		now = now.Add(30 * time.Second)
		c, _ := totp.GenerateCode(secret, now)
		return c
	}

	// A user with a known password.
	q := db.New(pool)
	tenant, _ := q.CreateTenant(ctx, "t")
	hash, _ := HashPassword("pw")
	u, err := q.CreateUser(ctx, db.CreateUserParams{TenantID: tenant.ID, Email: "u@test.local", PasswordHash: hash, Role: "user"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	userID := db.UUIDString(u.ID)

	// --- setup ---
	enrollmentApproval, err := svc.ApprovePasskeyWithPassword(ctx, userID, "pw", "", "totp:setup")
	if err != nil {
		t.Fatal(err)
	}
	url, tsecret, err := svc.SetupTOTP(ctx, userID, enrollmentApproval)
	if err != nil {
		t.Fatalf("SetupTOTP: %v", err)
	}
	if url == "" || tsecret == "" {
		t.Fatal("expected otpauth url + secret")
	}
	row, err := q.GetUserTOTP(ctx, u.ID)
	if err != nil {
		t.Fatalf("GetUserTOTP: %v", err)
	}
	if row.Enabled {
		t.Fatal("must not be enabled before confirm")
	}
	if row.Secret == tsecret {
		t.Fatal("secret stored in plaintext — must be encrypted")
	}

	// --- confirm with a generated code ---
	code := nextCode(tsecret)
	backup, err := svc.ConfirmTOTP(ctx, userID, code, enrollmentApproval)
	if err != nil {
		t.Fatalf("ConfirmTOTP: %v", err)
	}
	if len(backup) != backupCodeCount {
		t.Fatalf("expected %d backup codes, got %d", backupCodeCount, len(backup))
	}

	// --- login now requires a second factor ---
	res, err := svc.Login(ctx, "u@test.local", "pw")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if res.MFAToken == "" || res.Token != "" {
		t.Fatalf("expected MFA-pending login, got %+v", res)
	}

	// --- complete with a TOTP code ---
	code2 := nextCode(tsecret)
	done, err := svc.CompleteMFATOTP(ctx, res.MFAToken, code2)
	if err != nil {
		t.Fatalf("CompleteMFATOTP (totp): %v", err)
	}
	if done.Token == "" || done.Method != "totp" {
		t.Fatalf("expected a full session via totp, got %+v", done)
	}

	// --- complete with a one-time backup code, then prove reuse fails ---
	res2, _ := svc.Login(ctx, "u@test.local", "pw")
	viaBackup, err := svc.CompleteMFATOTP(ctx, res2.MFAToken, backup[0])
	if err != nil {
		t.Fatalf("CompleteMFATOTP (backup): %v", err)
	}
	if viaBackup.Method != "backup" {
		t.Fatalf("expected method=backup, got %q", viaBackup.Method)
	}
	res3, _ := svc.Login(ctx, "u@test.local", "pw")
	if _, err := svc.CompleteMFATOTP(ctx, res3.MFAToken, backup[0]); err == nil {
		t.Fatal("a used backup code must not work twice")
	}

	// --- regenerate backup codes: old (unused) ones stop working, new ones work ---
	regenCode := nextCode(tsecret)
	newCodes, err := svc.RegenerateBackupCodes(ctx, userID, regenCode)
	if err != nil || len(newCodes) != backupCodeCount {
		t.Fatalf("RegenerateBackupCodes: codes=%d err=%v", len(newCodes), err)
	}
	res4, _ := svc.Login(ctx, "u@test.local", "pw")
	if _, err := svc.CompleteMFATOTP(ctx, res4.MFAToken, backup[1]); err == nil {
		t.Fatal("an old backup code must stop working after regeneration")
	}
	res5, _ := svc.Login(ctx, "u@test.local", "pw")
	if _, err := svc.CompleteMFATOTP(ctx, res5.MFAToken, newCodes[0]); err != nil {
		t.Fatalf("a new backup code must work: %v", err)
	}
	if _, err := svc.RegenerateBackupCodes(ctx, userID, "000000"); err != ErrInvalidTOTPCode {
		t.Fatalf("regenerate with bad code: want ErrInvalidTOTPCode, got %v", err)
	}

	// --- disable requires password AND a current code ---
	code3 := nextCode(tsecret)
	if err := svc.DisableTOTP(ctx, userID, "wrong", code3); err != ErrInvalidCreds {
		t.Fatalf("disable with wrong password: err=%v, want ErrInvalidCreds", err)
	}
	if err := svc.DisableTOTP(ctx, userID, "pw", "000000"); err != ErrInvalidTOTPCode {
		t.Fatalf("disable with bad code: err=%v, want ErrInvalidTOTPCode", err)
	}
	if err := svc.DisableTOTP(ctx, userID, "pw", code3); err != nil {
		t.Fatalf("DisableTOTP: %v", err)
	}
	if _, err := q.GetUserTOTP(ctx, u.ID); err == nil {
		t.Fatal("user_totp row should be gone after disable")
	}
}
