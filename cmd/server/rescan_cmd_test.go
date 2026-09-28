package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"discodrive/internal/db"
)

func rescanCmdDB(t *testing.T) (*db.Queries, pgtype.UUID) {
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
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	q := db.New(pool)
	tenant, _ := q.CreateTenant(ctx, "t")
	u, err := q.CreateUser(ctx, db.CreateUserParams{TenantID: tenant.ID, Email: "a@x", PasswordHash: "x", Role: "user"})
	if err != nil {
		t.Fatal(err)
	}
	return q, u.ID
}

func TestRescanCommandQueuesForUser(t *testing.T) {
	q, uid := rescanCmdDB(t)
	var out bytes.Buffer
	if code := runRescan(context.Background(), q, []string{"--user", "a@x"}, &out, time.Millisecond); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	pending, _ := q.ListPendingRescanRequests(context.Background())
	if len(pending) != 1 || pending[0].UserID != uid || pending[0].RequestedBy != "cli" {
		t.Fatalf("pending %+v", pending)
	}
}

func TestRescanCommandUnknownUser(t *testing.T) {
	q, _ := rescanCmdDB(t)
	var out bytes.Buffer
	if code := runRescan(context.Background(), q, []string{"--user", "nobody@x"}, &out, time.Millisecond); code != 1 {
		t.Fatalf("exit %d, want 1: %s", code, out.String())
	}
}

// --wait returns once the running server has finished the request, printing its outcome.
func TestRescanCommandWaits(t *testing.T) {
	q, _ := rescanCmdDB(t)
	ctx := context.Background()
	go func() { // stands in for the server: finish whatever gets queued
		for {
			p, _ := q.ListPendingRescanRequests(ctx)
			if len(p) > 0 {
				_ = q.FinishRescanRequest(ctx, db.FinishRescanRequestParams{ID: p[0].ID, Imported: 3})
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	var out bytes.Buffer
	if code := runRescan(ctx, q, []string{"--wait"}, &out, 10*time.Millisecond); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "imported 3") {
		t.Fatalf("output %q lacks the outcome", out.String())
	}
}
