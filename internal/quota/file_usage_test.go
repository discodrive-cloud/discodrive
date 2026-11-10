package quota_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"discodrive/internal/db"
)

type usageFixture struct {
	t    *testing.T
	q    *db.Queries
	pool *pgxpool.Pool
	ctx  context.Context
}

func (f usageFixture) exec(sql string, args ...any) {
	f.t.Helper()
	if _, err := f.pool.Exec(f.ctx, sql, args...); err != nil {
		f.t.Fatalf("%s: %v", sql, err)
	}
}

func (f usageFixture) id(sql string, args ...any) pgtype.UUID {
	f.t.Helper()
	var id pgtype.UUID
	if err := f.pool.QueryRow(f.ctx, sql, args...).Scan(&id); err != nil {
		f.t.Fatalf("%s: %v", sql, err)
	}
	return id
}

func (f usageFixture) file(user pgtype.UUID, name string, size int64) pgtype.UUID {
	f.t.Helper()
	return f.id("INSERT INTO nodes (user_id, name, size) VALUES ($1, $2, $3) RETURNING id", user, name, size)
}

func (f usageFixture) want(step string, user pgtype.UUID, want int64) {
	f.t.Helper()
	got, err := f.q.UserStorageUsage(f.ctx, user)
	if err != nil {
		f.t.Fatal(err)
	}
	if got != want {
		f.t.Fatalf("%s: the user occupies %d bytes, want %d", step, got, want)
	}
}

// What a user occupies is asked on every write, so it is kept as a running count rather
// than summed over the file tree. The count has to follow everything that happens to
// files and their versions.
func TestFileUsageFollowsTheFileTree(t *testing.T) {
	q, pool, ctx := setupPool(t)
	f := usageFixture{t, q, pool, ctx}
	ann := makeUser(t, q, ctx, "ann@usage.test", nil)
	bob := makeUser(t, q, ctx, "bob@usage.test", nil)

	f.want("a new user", ann, 0)
	dir := f.id("INSERT INTO nodes (user_id, name, is_dir, size) VALUES ($1, 'docs', true, 4096) RETURNING id", ann)
	f.want("a folder takes nothing", ann, 0)
	a := f.file(ann, "a.txt", 100)
	f.exec("INSERT INTO nodes (user_id, name) VALUES ($1, 'sizeless')", ann)
	f.want("a file", ann, 100)

	f.exec("UPDATE nodes SET size = 250, version = version + 1 WHERE id = $1", a)
	f.want("an overwrite", ann, 250)
	f.exec("UPDATE nodes SET name = 'b.txt', parent_id = $2 WHERE id = $1", a, dir)
	f.exec("UPDATE nodes SET deleted_at = now() WHERE id = $1", a)
	f.want("renamed, moved, in the trash: still held", ann, 250)

	f.exec("INSERT INTO file_versions (node_id, version, size) VALUES ($1, 1, 40), ($1, 2, 60)", a)
	f.want("with two versions", ann, 350)
	f.exec("UPDATE file_versions SET size = 45 WHERE node_id = $1 AND version = 1", a)
	f.want("a version resized", ann, 355)
	f.exec("DELETE FROM file_versions WHERE node_id = $1 AND version = 1", a)
	f.want("a version trimmed", ann, 310)

	b := f.file(bob, "theirs.bin", 7000)
	f.want("another user's file", ann, 310)
	f.want("another user's file", bob, 7000)
	f.exec("INSERT INTO file_versions (node_id, version, size) VALUES ($1, 1, 500)", b)
	// Ownership moves with the file's history.
	f.exec("UPDATE nodes SET user_id = $2 WHERE id = $1", b, ann)
	f.want("a file handed over, with its version", ann, 7810)
	f.want("a file handed away", bob, 0)

	total, err := q.TotalStorageUsage(ctx)
	if err != nil || total != 7810 {
		t.Fatalf("the service occupies %d bytes (err %v), want 7810", total, err)
	}

	// Purged from the trash: the file goes, and its remaining versions with it.
	f.exec("DELETE FROM nodes WHERE id = $1", a)
	f.want("a file purged with its version", ann, 7500)
	f.exec("UPDATE nodes SET is_dir = true WHERE id = $1", b)
	f.want("a file turned folder keeps only its version", ann, 500)

	// A deleted account leaves nothing behind in the total.
	f.exec("DELETE FROM users WHERE id = $1", ann)
	if total, err := q.TotalStorageUsage(ctx); err != nil || total != 0 {
		t.Fatalf("after the account is gone the service occupies %d bytes (err %v)", total, err)
	}
}

// The count is only as good as what maintains it. A pass over the file tree compares the
// two, corrects the count and reports the difference — so a restore that skipped the
// bookkeeping, or a bug in it, costs one logged line rather than a wrong quota for good.
func TestReconcileFileUsageRepairsADriftedCount(t *testing.T) {
	q, pool, ctx := setupPool(t)
	f := usageFixture{t, q, pool, ctx}
	ann := makeUser(t, q, ctx, "ann@drift.test", nil)
	bob := makeUser(t, q, ctx, "bob@drift.test", nil)
	f.file(ann, "counted.bin", 1000)
	f.file(bob, "fine.bin", 300)

	if drift, err := q.ReconcileFileUsage(ctx); err != nil || len(drift) != 0 {
		t.Fatalf("a count in step was corrected: %v (err %v)", drift, err)
	}

	// Rows that arrive behind the bookkeeping's back, as a data-only restore would bring them.
	f.exec("ALTER TABLE nodes DISABLE TRIGGER USER")
	f.file(ann, "smuggled.bin", 5000)
	f.exec("ALTER TABLE nodes ENABLE TRIGGER USER")

	drift, err := q.ReconcileFileUsage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(drift) != 1 || drift[0].UserID != ann || drift[0].Bytes != 5000 {
		t.Fatalf("reported drift %+v, want 5000 bytes for the one user", drift)
	}
	f.want("after the correction", ann, 6000)
	f.want("the user in step", bob, 300)
	if drift, err := q.ReconcileFileUsage(ctx); err != nil || len(drift) != 0 {
		t.Fatalf("a second pass corrected again: %v (err %v)", drift, err)
	}
}

// Every change to a file adds a row to the count; folding keeps it at one row a user,
// and what the user occupies does not change by it.
func TestFoldFileUsageKeepsOneRowAUser(t *testing.T) {
	q, pool, ctx := setupPool(t)
	f := usageFixture{t, q, pool, ctx}
	ann := makeUser(t, q, ctx, "ann@fold.test", nil)
	bob := makeUser(t, q, ctx, "bob@fold.test", nil)
	for i := 0; i < 50; i++ {
		f.file(ann, "f"+string(rune('a'+i%26))+string(rune('a'+i/26)), 10)
	}
	gone := f.file(bob, "gone.bin", 77)
	f.exec("DELETE FROM nodes WHERE id = $1", gone)

	if err := q.FoldFileUsage(ctx); err != nil {
		t.Fatal(err)
	}
	rows := func(user pgtype.UUID) (n int) {
		t.Helper()
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM file_usage_deltas WHERE user_id = $1", user).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := rows(ann); n != 1 {
		t.Errorf("%d rows are kept for a user after folding, want 1", n)
	}
	if n := rows(bob); n != 0 {
		t.Errorf("%d rows are kept for a user who occupies nothing", n)
	}
	f.want("after folding", ann, 500)
	f.want("after folding", bob, 0)
}
