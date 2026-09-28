package storage_test

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"discodrive/internal/db"
	"discodrive/internal/storage"
)

func i64(v int64) *int64 { return &v }

// setupFS spins up a throwaway Postgres container, applies migrations, and returns
// a ready FileService + Queries + test user ID + disk root.
func setupFS(t *testing.T) (*storage.FileService, *db.Queries, string, string) {
	t.Helper()
	return setupFSWith(t, nil)
}

// setupFSWith is setupFS with the disk wrapped by wrap (nil = plain LocalDisk).
func setupFSWith(t *testing.T, wrap func(storage.Storage) storage.Storage) (*storage.FileService, *db.Queries, string, string) {
	t.Helper()
	fs, q, _, userID, root := setupFSPool(t, wrap)
	return fs, q, userID, root
}

// setupFSPool is setupFSWith that also returns the pool, for tests that need their own
// transactions.
func setupFSPool(t *testing.T, wrap func(storage.Storage) storage.Storage) (*storage.FileService, *db.Queries, *pgxpool.Pool, string, string) {
	t.Helper()
	ctx := context.Background()

	pgC, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("kf"),
		tcpostgres.WithUsername("kf"),
		tcpostgres.WithPassword("kf"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Skipf("could not start a postgres container (Docker required): %v", err)
	}
	t.Cleanup(func() { _ = pgC.Terminate(ctx) })

	dsn, err := pgC.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("dsn: %v", err)
	}
	if err := db.MigrateUp(dsn); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)

	q := db.New(pool)
	tenant, err := q.CreateTenant(ctx, "t")
	if err != nil {
		t.Fatalf("tenant: %v", err)
	}
	user, err := q.CreateUser(ctx, db.CreateUserParams{
		TenantID: tenant.ID, Email: "u@x", PasswordHash: "x", Role: "user",
	})
	if err != nil {
		t.Fatalf("user: %v", err)
	}
	root := t.TempDir()
	var st storage.Storage = storage.NewLocalDisk(root)
	if wrap != nil {
		st = wrap(st)
	}
	fs := storage.NewFileService(pool, st)
	return fs, q, pool, db.UUIDString(user.ID), root
}

func readNode(t *testing.T, fs *storage.FileService, userID, nodeID string) string {
	t.Helper()
	_, f, err := fs.Open(context.Background(), userID, nodeID)
	if err != nil {
		t.Fatalf("open %s: %v", nodeID, err)
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(b)
}

// Core of the sync protocol: two devices push from the same base_version,
// one wins, the other becomes a conflict copy, and NOT A SINGLE BYTE is lost.
func TestPushConflict_NoDataLoss(t *testing.T) {
	ctx := context.Background()
	fs, q, userID, _ := setupFS(t)

	base, err := fs.Push(ctx, userID, nil, "note.txt", nil, "init", strings.NewReader("base"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if base.Conflicted {
		t.Fatal("creation must not be a conflict")
	}
	if base.Node.Version != 1 {
		t.Fatalf("version=%d, expected 1", base.Node.Version)
	}

	// device A: base_version=1 matches current → accepted (version 2)
	resA, err := fs.Push(ctx, userID, nil, "note.txt", i64(1), "deviceA", strings.NewReader("AAA"))
	if err != nil {
		t.Fatalf("push A: %v", err)
	}
	if resA.Conflicted {
		t.Fatal("first push must not conflict")
	}
	if resA.Node.Version != 2 {
		t.Fatalf("version A=%d, expected 2", resA.Node.Version)
	}

	// device B: base_version=1 is stale (now 2) → CONFLICT
	resB, err := fs.Push(ctx, userID, nil, "note.txt", i64(1), "deviceB", strings.NewReader("BBB"))
	if err != nil {
		t.Fatalf("push B: %v", err)
	}
	if !resB.Conflicted {
		t.Fatal("second push must be a conflict")
	}
	if !resB.Node.IsConflictLoser {
		t.Fatal("conflict copy must have is_conflict_loser=true")
	}
	if resB.Node.ID == base.Node.ID {
		t.Fatal("conflict copy must be a SEPARATE node")
	}
	if !strings.Contains(resB.Node.Name, "conflict") {
		t.Fatalf("copy name without a conflict marker: %q", resB.Node.Name)
	}

	// NOT A SINGLE BYTE LOST: primary = AAA (winner), copy = BBB
	if got := readNode(t, fs, userID, db.UUIDString(base.Node.ID)); got != "AAA" {
		t.Fatalf("main file = %q, expected AAA (winner)", got)
	}
	if got := readNode(t, fs, userID, db.UUIDString(resB.Node.ID)); got != "BBB" {
		t.Fatalf("conflict copy = %q, expected BBB", got)
	}

	// exactly two nodes at root: original and conflict copy
	uid, _ := db.ParseUUID(userID)
	roots, err := q.ListRootNodes(ctx, uid)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(roots) != 2 {
		t.Fatalf("nodes at root=%d, expected 2", len(roots))
	}
}
