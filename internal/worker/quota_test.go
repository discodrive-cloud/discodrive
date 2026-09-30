package worker_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"discodrive/internal/db"
)

func TestQuotaCandidatesAndMark(t *testing.T) {
	ctx := context.Background()
	pgC, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("kf"), tcpostgres.WithUsername("kf"), tcpostgres.WithPassword("kf"),
		tcpostgres.BasicWaitStrategies())
	if err != nil {
		t.Skipf("Docker required: %v", err)
	}
	t.Cleanup(func() { _ = pgC.Terminate(ctx) })
	dsn, _ := pgC.ConnectionString(ctx, "sslmode=disable")
	if err := db.MigrateUp(dsn); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	pool, _ := pgxpool.New(ctx, dsn)
	t.Cleanup(pool.Close)
	q := db.New(pool)
	tenant, _ := q.CreateTenant(ctx, "t")
	u, _ := q.CreateUser(ctx, db.CreateUserParams{TenantID: tenant.ID, Email: "u@x", PasswordHash: "x", Role: "user"})

	if _, err := pool.Exec(ctx, "UPDATE users SET storage_quota=100, storage_used=95 WHERE id=$1", u.ID); err != nil {
		t.Fatalf("update: %v", err)
	}
	cands, _ := q.ListQuotaCandidates(ctx)
	if len(cands) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(cands))
	}
	if err := q.MarkQuotaNotified(ctx, u.ID); err != nil {
		t.Fatalf("mark: %v", err)
	}
	if c2, _ := q.ListQuotaCandidates(ctx); len(c2) != 0 {
		t.Fatalf("expected 0 after mark, got %d", len(c2))
	}
	if _, err := pool.Exec(ctx, "UPDATE users SET storage_used=10 WHERE id=$1", u.ID); err != nil {
		t.Fatalf("update2: %v", err)
	}
	if err := q.ClearQuotaNotified(ctx); err != nil {
		t.Fatalf("clear: %v", err)
	}
	var notified pgtype.Timestamptz
	_ = pool.QueryRow(ctx, "SELECT quota_notified_at FROM users WHERE id=$1", u.ID).Scan(&notified)
	if notified.Valid {
		t.Fatal("quota_notified_at must reset to NULL")
	}

	// Nothing on the write path maintains storage_used; the quota job refreshes it from
	// the live totals. Without that refresh the column stays at 0 forever and the
	// "near limit" notification never fires — which is exactly what it used to do.
	node, err := q.CreateNode(ctx, db.CreateNodeParams{
		UserID: u.ID, Name: "f.bin", IsDir: false,
		Size: pgtype.Int8{Int64: 700, Valid: true}, DiskPath: pgtype.Text{String: "f.bin", Valid: true},
	})
	if err != nil {
		t.Fatalf("node: %v", err)
	}
	if _, err := q.InsertFileVersion(ctx, db.InsertFileVersionParams{
		NodeID: node.ID, Version: 1, Size: pgtype.Int8{Int64: 300, Valid: true},
	}); err != nil {
		t.Fatalf("version: %v", err)
	}
	if err := q.RefreshStorageUsed(ctx); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	var used int64
	if err := pool.QueryRow(ctx, "SELECT storage_used FROM users WHERE id=$1", u.ID).Scan(&used); err != nil {
		t.Fatalf("read storage_used: %v", err)
	}
	if used != 1000 { // 700 live + 300 in a version snapshot
		t.Fatalf("storage_used = %d, want 1000", used)
	}
}
