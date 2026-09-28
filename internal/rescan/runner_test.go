package rescan_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"discodrive/internal/db"
	"discodrive/internal/rescan"
	"discodrive/internal/storage"
)

type env struct {
	pool *pgxpool.Pool
	q    *db.Queries
	fs   *storage.FileService
	root string
}

func newEnv(t *testing.T) *env {
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
	root := t.TempDir()
	return &env{pool: pool, q: db.New(pool), fs: storage.NewFileService(pool, storage.NewLocalDisk(root)), root: root}
}

// user creates a user and drops one file into their tree outside the service.
func (e *env) user(t *testing.T, email string) pgtype.UUID {
	t.Helper()
	ctx := context.Background()
	tenant, _ := e.q.CreateTenant(ctx, email)
	u, err := e.q.CreateUser(ctx, db.CreateUserParams{TenantID: tenant.ID, Email: email, PasswordHash: "x", Role: "user"})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(e.root, db.UUIDString(u.ID))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "dropped.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	return u.ID
}

func finished(t *testing.T, e *env, id int64) db.RescanRequest {
	t.Helper()
	r, err := e.q.GetRescanRequest(context.Background(), id)
	if err != nil || !r.FinishedAt.Valid {
		t.Fatalf("request %d not finished: %+v %v", id, r, err)
	}
	return r
}

func TestDrainExecutesAndRecords(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	uid := e.user(t, "a@x")
	id, err := rescan.Enqueue(ctx, e.q, uid, "cli")
	if err != nil {
		t.Fatal(err)
	}
	if err := rescan.NewRunner(e.pool, e.fs).Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if r := finished(t, e, id); r.Imported != 1 || !r.StartedAt.Valid {
		t.Fatalf("request %+v: want started, imported 1", r)
	}
}

// Identical requests pending together get one reconciliation and the same outcome.
func TestDrainMergesDuplicates(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	uid := e.user(t, "a@x")
	first, _ := rescan.Enqueue(ctx, e.q, uid, "cli")
	second, _ := rescan.Enqueue(ctx, e.q, uid, "admin:x")
	if err := rescan.NewRunner(e.pool, e.fs).Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if a, b := finished(t, e, first), finished(t, e, second); a.Imported != 1 || b.Imported != 1 {
		t.Fatalf("outcomes %d and %d, want 1 and 1 (one run, shared result)", a.Imported, b.Imported)
	}
}

// A request for every user (NULL user_id) sums the per-user outcomes.
func TestDrainAllUsers(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.user(t, "a@x")
	e.user(t, "b@x")
	id, _ := rescan.Enqueue(ctx, e.q, pgtype.UUID{}, "startup")
	if err := rescan.NewRunner(e.pool, e.fs).Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if r := finished(t, e, id); r.Imported != 2 {
		t.Fatalf("imported %d, want 2", r.Imported)
	}
}

// A request started but not finished before a crash runs again.
func TestDrainResumesInterrupted(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	uid := e.user(t, "a@x")
	id, _ := rescan.Enqueue(ctx, e.q, uid, "cli")
	if err := e.q.StartRescanRequest(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := rescan.NewRunner(e.pool, e.fs).Drain(ctx); err != nil {
		t.Fatal(err)
	}
	finished(t, e, id)
}

// Run picks up a request inserted while it is listening.
func TestRunWakesOnNotify(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go rescan.NewRunner(e.pool, e.fs).Run(ctx)
	time.Sleep(200 * time.Millisecond) // let it LISTEN
	uid := e.user(t, "a@x")
	id, _ := rescan.Enqueue(ctx, e.q, uid, "cli")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if r, _ := e.q.GetRescanRequest(ctx, id); r.FinishedAt.Valid {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("request not executed within 5 s of being queued")
}
