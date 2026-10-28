package api_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"discodrive/internal/api"
	"discodrive/internal/auth"
	"discodrive/internal/db"
	"discodrive/internal/quota"
	"discodrive/internal/storage"
)

// A user has to be able to see their own space: an upload refused with "not enough
// storage left" is only actionable when the numbers behind it are visible without
// asking an admin.
func TestMeStorage(t *testing.T) {
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
	svc := auth.NewService(pool, auth.NewTokenIssuer("secret", time.Hour), nil)
	tok, user, err := svc.Register(ctx, "u@x.test", "password12")
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	files := storage.NewFileService(pool, storage.NewLocalDisk(t.TempDir()))
	files.SetQuota(quota.New(q, 0))
	h := api.NewRouter(svc, q, files, nil, t.TempDir(), nil, nil, nil, nil, nil, nil, nil,
		false, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	get := func() map[string]any {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/me/storage", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /me/storage = %d %s", rec.Code, rec.Body.String())
		}
		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return out
	}

	// No quota and no server cap: a number to show, and nothing constraining it.
	out := get()
	if out["used"].(float64) != 0 {
		t.Fatalf("used = %v, want 0", out["used"])
	}
	if out["quota"] != nil || out["available"] != nil {
		t.Fatalf("an unconstrained user must report null quota and null available, got %v", out)
	}

	node, err := q.CreateNode(ctx, db.CreateNodeParams{
		UserID: user.ID, Name: "f.bin", IsDir: false,
		Size: pgtype.Int8{Int64: 400, Valid: true}, DiskPath: pgtype.Text{String: "f.bin", Valid: true},
	})
	if err != nil {
		t.Fatalf("node: %v", err)
	}
	if _, err := q.UpdateUser(ctx, db.UpdateUserParams{
		ID: user.ID, Role: "user", StorageQuota: pgtype.Int8{Int64: 1000, Valid: true},
	}); err != nil {
		t.Fatalf("set quota: %v", err)
	}

	out = get()
	if out["used"].(float64) != 400 {
		t.Fatalf("used = %v, want 400", out["used"])
	}
	if out["quota"].(float64) != 1000 {
		t.Fatalf("quota = %v, want 1000", out["quota"])
	}
	if out["available"].(float64) != 600 {
		t.Fatalf("available = %v, want 600", out["available"])
	}
	if out["trash"].(float64) != 0 {
		t.Fatalf("trash = %v, want 0", out["trash"])
	}

	// Deleting only moves the file to the trash: it keeps costing quota, and the trash
	// figure is what tells the user they can get it back by emptying it.
	if err := q.SoftDeleteNode(ctx, node.ID); err != nil {
		t.Fatalf("soft delete: %v", err)
	}
	out = get()
	if out["used"].(float64) != 400 {
		t.Fatalf("trashed file must still count as used, got %v", out["used"])
	}
	if out["trash"].(float64) != 400 {
		t.Fatalf("trash = %v, want 400", out["trash"])
	}
}

// The sync clients (desktop daemon, iOS) send a whole file in one PUT with a known
// length. A file that cannot fit has to be refused on the spot: the daemon retries a
// failed push every cycle, so answering only after the last byte means re-uploading it
// in full, forever, while the quota stays full.
func TestSyncPut_RefusedBeforeTheBody(t *testing.T) {
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
	svc := auth.NewService(pool, auth.NewTokenIssuer("secret", time.Hour), nil)
	tok, user, err := svc.Register(ctx, "u@x.test", "password12")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, err := q.UpdateUser(ctx, db.UpdateUserParams{
		ID: user.ID, Role: "user", StorageQuota: pgtype.Int8{Int64: 100, Valid: true},
	}); err != nil {
		t.Fatalf("set quota: %v", err)
	}

	files := storage.NewFileService(pool, storage.NewLocalDisk(t.TempDir()))
	files.SetQuota(quota.New(q, 0))
	h := api.NewRouter(svc, q, files, nil, t.TempDir(), nil, nil, nil, nil, nil, nil, nil,
		false, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	body := &countingReader{n: 5000}
	req := httptest.NewRequest(http.MethodPut, "/sync/file?path=big.bin", body)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.ContentLength = 5000
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusInsufficientStorage {
		t.Fatalf("PUT /sync/file = %d, want 507 (%s)", rec.Code, rec.Body.String())
	}
	if body.read > 0 {
		t.Fatalf("the body must not be read at all, got %d bytes", body.read)
	}
}

// countingReader reports how much of the body the server actually consumed.
type countingReader struct {
	n    int
	read int
}

func (c *countingReader) Read(p []byte) (int, error) {
	if c.read >= c.n {
		return 0, io.EOF
	}
	k := min(len(p), c.n-c.read)
	c.read += k
	return k, nil
}

// A user without a personal quota must be told what they occupy and nothing else. The
// server-wide cap is the operator's number: reporting it here showed a regular user the
// shared disk's free space as though it were their own allowance.
func TestMeStorage_DoesNotLeakTheServerCap(t *testing.T) {
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
	svc := auth.NewService(pool, auth.NewTokenIssuer("secret", time.Hour), nil)
	tok, user, err := svc.Register(ctx, "u@x.test", "password12")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, err := q.CreateNode(ctx, db.CreateNodeParams{
		UserID: user.ID, Name: "f.bin", IsDir: false,
		Size: pgtype.Int8{Int64: 200, Valid: true}, DiskPath: pgtype.Text{String: "f.bin", Valid: true},
	}); err != nil {
		t.Fatalf("node: %v", err)
	}

	files := storage.NewFileService(pool, storage.NewLocalDisk(t.TempDir()))
	files.SetQuota(quota.New(q, 1000)) // server cap, no personal quota
	h := api.NewRouter(svc, q, files, nil, t.TempDir(), nil, nil, nil, nil, nil, nil, nil,
		false, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/me/storage", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /me/storage = %d %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out["quota"] != nil {
		t.Fatalf("quota must stay null, got %v", out["quota"])
	}
	if out["available"] != nil {
		t.Fatalf("available must stay null under a server cap, got %v", out["available"])
	}
	if out["used"].(float64) != 200 {
		t.Fatalf("used = %v, want 200", out["used"])
	}
}
