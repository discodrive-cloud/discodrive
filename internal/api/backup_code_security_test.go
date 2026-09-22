package api

import (
	"context"
	"testing"
	"time"

	"discodrive/internal/auth"
	"discodrive/internal/db"
)

func TestBackupCodeConcurrentUseHasOneWinner(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, q, svc := bootstrapPairingDB(t)
	_, u, err := svc.Register(ctx, "backup@example.test", "test-password")
	if err != nil {
		t.Fatal(err)
	}
	hash, err := auth.HashPassword("abcdefgh")
	if err != nil {
		t.Fatal(err)
	}
	if err = q.InsertBackupCode(ctx, db.InsertBackupCodeParams{UserID: u.ID, CodeHash: hash}); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, "SELECT id FROM backup_codes WHERE user_id=$1 FOR UPDATE", u.ID); err != nil {
		t.Fatal(err)
	}
	issuer := auth.NewTokenIssuer("secret", time.Hour)
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		// Distinct MFA challenges exercise the backup-code race independently of
		// the separate one-use guard on each challenge.
		token, err := issuer.IssueMFA(db.UUIDString(u.ID), db.UUIDString(u.TenantID), u.TokenVersion)
		if err != nil {
			t.Fatal(err)
		}
		go func() { _, err := svc.CompleteMFATOTP(ctx, token, "abcd-efgh"); results <- err }()
	}
	// Hold the row until both consumers have read the same unused code and are
	// blocked trying to claim it. This deterministically reproduces the race.
	for {
		var waiting int
		err = pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE wait_event_type='Lock' AND query LIKE '%UPDATE backup_codes SET used_at%'`).Scan(&waiting)
		if err != nil {
			t.Fatal(err)
		}
		if waiting == 2 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("consumers did not reach claim barrier")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	wins := 0
	for i := 0; i < 2; i++ {
		select {
		case err := <-results:
			if err == nil {
				wins++
			} else if err != auth.ErrInvalidTOTPCode {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if wins != 1 {
		t.Fatalf("single-use backup code admitted %d sessions", wins)
	}
}
