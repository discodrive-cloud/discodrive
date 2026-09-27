package db_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"discodrive/internal/db"
)

// GetLiveNodeByPath runs for every WebDAV entry and sync path; without an index on
// (user_id, disk_path) each call scans the whole nodes table.
func TestLiveNodePathIndex(t *testing.T) {
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

	var def string
	err = pool.QueryRow(ctx, `SELECT indexdef FROM pg_indexes WHERE tablename = 'nodes' AND indexname = 'nodes_live_path'`).Scan(&def)
	if err != nil {
		t.Fatalf("nodes_live_path index missing: %v", err)
	}
	if !strings.Contains(def, "(user_id, disk_path)") || !strings.Contains(def, "deleted_at IS NULL") {
		t.Fatalf("unexpected index definition: %s", def)
	}

	// The planner must be able to answer the lookup from the index alone. SET is
	// per session, so both statements run on one connection.
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "SET enable_seqscan = off"); err != nil {
		t.Fatal(err)
	}
	rows, err := conn.Query(ctx, `EXPLAIN SELECT * FROM nodes WHERE user_id = gen_random_uuid() AND disk_path = 'a/b' AND deleted_at IS NULL`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var line string
		_ = rows.Scan(&line)
		plan.WriteString(line + "\n")
	}
	if !strings.Contains(plan.String(), "nodes_live_path") {
		t.Fatalf("lookup does not use nodes_live_path:\n%s", plan.String())
	}
}
