package db_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"discodrive/internal/db"
)

// A browser session issued before the account's tokens were revoked can never be used
// again; such rows are collected, and live sessions are left alone.
func TestDeleteDeadBrowserSessions(t *testing.T) {
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
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	q := db.New(pool)
	tenant, _ := q.CreateTenant(ctx, "t")
	u, err := q.CreateUser(ctx, db.CreateUserParams{TenantID: tenant.ID, Email: "s@test.local", PasswordHash: "x", Role: "user"})
	if err != nil {
		t.Fatal(err)
	}
	for id, ver := range map[string]int64{"before-the-revocation": u.TokenVersion, "after-it": u.TokenVersion + 1} {
		if err := q.CreateBrowserSession(ctx, db.CreateBrowserSessionParams{ID: id, UserID: u.ID, TokenVersion: ver}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, "UPDATE users SET token_version = token_version + 1 WHERE id = $1", u.ID); err != nil {
		t.Fatal(err)
	}
	n, err := q.DeleteDeadBrowserSessions(ctx)
	if err != nil || n != 1 {
		t.Fatalf("collected %d sessions (err %v), want the one revoked", n, err)
	}
	live, err := q.BrowserSessionActive(ctx, db.BrowserSessionActiveParams{ID: "after-it", UserID: u.ID, TokenVersion: u.TokenVersion + 1})
	if err != nil || !live {
		t.Fatalf("the live session was collected (active=%v, err %v)", live, err)
	}
}
