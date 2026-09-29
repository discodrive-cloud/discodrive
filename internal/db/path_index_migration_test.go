package db_test

import (
	"context"
	"strings"
	"testing"
)

// GetLiveNodeByPath runs for every WebDAV entry and sync path; without an index on
// (user_id, disk_path) each call scans the whole nodes table.
func TestLiveNodePathIndex(t *testing.T) {
	ctx := context.Background()
	pool := migratedPool(t)

	var def string
	err := pool.QueryRow(ctx, `SELECT indexdef FROM pg_indexes WHERE tablename = 'nodes' AND indexname = 'nodes_live_path'`).Scan(&def)
	if err != nil {
		t.Fatalf("nodes_live_path index missing: %v", err)
	}
	// text_pattern_ops since migration 000021: equality lookups and subtree ranges.
	if !strings.Contains(def, "(user_id, disk_path text_pattern_ops)") || !strings.Contains(def, "deleted_at IS NULL") {
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
