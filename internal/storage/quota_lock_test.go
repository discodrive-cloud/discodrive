package storage_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"discodrive/internal/db"
	"discodrive/internal/quota"
)

// lockTaken reports whether user's quota lock can be taken by a second transaction while
// holder keeps it for holderUser.
func quotaLockBlocks(t *testing.T, pool *pgxpool.Pool, c *quota.Checker, holderUser, user pgtype.UUID) bool {
	t.Helper()
	ctx := context.Background()
	holder, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback(ctx)
	if err := c.Lock(ctx, db.New(holder), holderUser); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		tx, err := pool.Begin(ctx)
		if err != nil {
			done <- err
			return
		}
		defer tx.Rollback(ctx)
		done <- c.Lock(ctx, db.New(tx), user)
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
		return false
	case <-time.After(500 * time.Millisecond):
		_ = holder.Rollback(ctx)
		<-done
		return true
	}
}

// The quota check of one user must not wait for another user's: with per-user quotas
// only, each user's writes are serialized among themselves. A server-wide cap is shared
// by everyone, so with one configured the checks do queue up.
func TestQuotaLockIsPerUserUnlessThereIsAServerCap(t *testing.T) {
	_, q, pool, userID, _ := setupFSPool(t, nil)
	a, _ := db.ParseUUID(userID)
	other, err := q.CreateUser(context.Background(), db.CreateUserParams{
		TenantID: mustTenant(t, q, userID), Email: "other@x", PasswordHash: "x", Role: "user",
	})
	if err != nil {
		t.Fatal(err)
	}
	b := other.ID

	perUser := quota.New(q, 0)
	if !quotaLockBlocks(t, pool, perUser, a, a) {
		t.Error("two quota checks of one user ran at once")
	}
	if quotaLockBlocks(t, pool, perUser, a, b) {
		t.Error("a user's quota check waited for another user's")
	}
	capped := quota.New(q, 1<<40)
	if !quotaLockBlocks(t, pool, capped, a, b) {
		t.Error("with a server-wide cap two users' quota checks ran at once")
	}
}
