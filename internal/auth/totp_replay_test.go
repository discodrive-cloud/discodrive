package auth

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pquerna/otp/totp"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"discodrive/internal/db"
	"discodrive/internal/secret"
)

// newTOTPUser returns a service and a user "r@test.local" / "pw" with TOTP enabled,
// plus the plaintext secret and a settable clock.
func newTOTPUser(t *testing.T) (svc *Service, userID, tsecret string, now *time.Time) {
	t.Helper()
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
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	cipher, err := secret.New("0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	svc = NewService(pool, NewTokenIssuer("secret", time.Hour), cipher)
	clock := time.Now()
	totpNow = func() time.Time { return clock }
	t.Cleanup(func() { totpNow = time.Now })

	q := db.New(pool)
	tenant, _ := q.CreateTenant(ctx, "t")
	hash, _ := HashPassword("pw")
	u, err := q.CreateUser(ctx, db.CreateUserParams{TenantID: tenant.ID, Email: "r@test.local", PasswordHash: hash, Role: "user"})
	if err != nil {
		t.Fatal(err)
	}
	userID = db.UUIDString(u.ID)
	approval, err := svc.ApprovePasskeyWithPassword(ctx, userID, "pw", "", "totp:setup")
	if err != nil {
		t.Fatal(err)
	}
	if _, tsecret, err = svc.SetupTOTP(ctx, userID, approval); err != nil {
		t.Fatal(err)
	}
	code, _ := totp.GenerateCode(tsecret, clock)
	if _, err := svc.ConfirmTOTP(ctx, userID, code, approval); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(30 * time.Second)
	return svc, userID, tsecret, &clock
}

// A TOTP code stays valid ~90 s. It used to be accepted any number of times in that
// window: an observed or phished code could sign in again, disable 2FA or mint new
// backup codes. Every place that checks a code must accept it once.
func TestTOTPCodeIsSingleUse(t *testing.T) {
	ctx := context.Background()
	svc, userID, tsecret, now := newTOTPUser(t)
	code, _ := totp.GenerateCode(tsecret, *now)

	res, err := svc.Login(ctx, "r@test.local", "pw")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CompleteMFATOTP(ctx, res.MFAToken, code); err != nil {
		t.Fatalf("first use of a fresh code: %v", err)
	}

	res2, _ := svc.Login(ctx, "r@test.local", "pw")
	if _, err := svc.CompleteMFATOTP(ctx, res2.MFAToken, code); err == nil {
		t.Fatal("login MFA: a used code was accepted again")
	}
	if _, err := svc.RegenerateBackupCodes(ctx, userID, code); err != ErrInvalidTOTPCode {
		t.Fatalf("regenerate backup codes: replayed code gave %v, want ErrInvalidTOTPCode", err)
	}
	if _, err := svc.ApprovePasskeyWithPassword(ctx, userID, "pw", code, "register"); err != ErrApproval {
		t.Fatalf("identity approval: replayed code gave %v, want ErrApproval", err)
	}
	if err := svc.DisableTOTP(ctx, userID, "pw", code); err != ErrInvalidTOTPCode {
		t.Fatalf("disable: replayed code gave %v, want ErrInvalidTOTPCode", err)
	}
	// An older step (still inside the skew window) is refused too.
	older, _ := totp.GenerateCode(tsecret, now.Add(-30*time.Second))
	if _, err := svc.RegenerateBackupCodes(ctx, userID, older); err != ErrInvalidTOTPCode {
		t.Fatalf("an earlier step than the last used one was accepted: %v", err)
	}

	// The next step's code works.
	*now = now.Add(30 * time.Second)
	next, _ := totp.GenerateCode(tsecret, *now)
	if _, err := svc.RegenerateBackupCodes(ctx, userID, next); err != nil {
		t.Fatalf("a fresh code after the used one: %v", err)
	}
}

// Two concurrent sign-ins with the same fresh code: exactly one may win.
func TestTOTPConcurrentReplay(t *testing.T) {
	ctx := context.Background()
	svc, _, tsecret, now := newTOTPUser(t)
	code, _ := totp.GenerateCode(tsecret, *now)
	const n = 6
	tokens := make([]string, n)
	for i := range tokens {
		res, err := svc.Login(ctx, "r@test.local", "pw")
		if err != nil {
			t.Fatal(err)
		}
		tokens[i] = res.MFAToken
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for _, tok := range tokens {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := svc.CompleteMFATOTP(ctx, tok, code); err == nil {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("%d concurrent sign-ins succeeded with one code, want exactly 1", wins)
	}
}
