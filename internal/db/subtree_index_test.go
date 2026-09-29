package db

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

// Folder rename, move and delete select a subtree by path. Under the database's en_US
// collation starts_with(...) could not use any index, so each of them read the whole
// nodes table. The generated statements must be answerable from the path indexes —
// also as generic plans, which is what prepared statements end up with.
func TestSubtreeQueriesUsePathIndexes(t *testing.T) {
	ctx := context.Background()
	pgC, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("kf"), tcpostgres.WithUsername("kf"), tcpostgres.WithPassword("kf"),
		tcpostgres.BasicWaitStrategies())
	if err != nil {
		t.Skipf("need Docker: %v", err)
	}
	t.Cleanup(func() { _ = pgC.Terminate(ctx) })
	dsn, _ := pgC.ConnectionString(ctx, "sslmode=disable")
	if err := MigrateUp(dsn); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "SET enable_seqscan = off"); err != nil {
		t.Fatal(err)
	}
	for name, sql := range map[string]string{
		"RewriteSubtreePaths":  rewriteSubtreePaths,
		"SoftDeleteSubtree":    softDeleteSubtree,
		"RecordSubtreeChanges": recordSubtreeChanges,
	} {
		// Simple protocol: the statement keeps its $n placeholders unbound.
		res, err := conn.Conn().PgConn().Exec(ctx, "EXPLAIN (GENERIC_PLAN) "+sql).ReadAll()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var plan strings.Builder
		for _, r := range res {
			for _, row := range r.Rows {
				plan.Write(row[0])
				plan.WriteString("\n")
			}
		}
		if plan.Len() == 0 {
			t.Fatalf("%s: empty plan", name)
		}
		if strings.Contains(plan.String(), "Seq Scan on nodes") {
			t.Fatalf("%s scans the nodes table:\n%s", name, plan.String())
		}
	}
}
