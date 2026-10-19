package webdav

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"discodrive/internal/db"
	"discodrive/internal/quota"
	"discodrive/internal/storage"
)

// A PUT that cannot fit must be refused with 507 before the body is read: a desktop
// client syncing a folder bigger than the quota should be told immediately, not after
// pushing gigabytes that get thrown away at commit time.
func TestPut_RefusedWith507WhenOverQuota(t *testing.T) {
	ctx := context.Background()
	pgC, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("kf"), tcpostgres.WithUsername("kf"), tcpostgres.WithPassword("kf"),
		tcpostgres.BasicWaitStrategies())
	if err != nil {
		t.Skipf("requires Docker: %v", err)
	}
	t.Cleanup(func() { _ = pgC.Terminate(ctx) })
	dsn, _ := pgC.ConnectionString(ctx, "sslmode=disable")
	if err := db.MigrateUp(dsn); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)

	q := db.New(pool)
	tenant, _ := q.CreateTenant(ctx, "t")
	user, err := q.CreateUser(ctx, db.CreateUserParams{
		TenantID: tenant.ID, Email: "u@x", PasswordHash: "x", Role: "user",
		StorageQuota: pgtype.Int8{Int64: 1000, Valid: true},
	})
	if err != nil {
		t.Fatalf("user: %v", err)
	}
	uid := db.UUIDString(user.ID)

	svc := storage.NewFileService(pool, storage.NewLocalDisk(t.TempDir()))
	svc.SetQuota(quota.New(q, 0))
	h := Handler(svc, "/dav")

	put := func(name string, size int) *httptest.ResponseRecorder {
		body := strings.Repeat("x", size)
		req := httptest.NewRequest(http.MethodPut, "/dav/"+name, strings.NewReader(body))
		req.ContentLength = int64(size)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req.WithContext(context.WithValue(ctx, ctxUserKey, uid)))
		return rec
	}

	// A chunked body declares no length, so the up-front check cannot refuse it; it used to
	// be spooled to the temp dir in full before Push applied the quota.
	chunked := httptest.NewRequest(http.MethodPut, "/dav/chunked.bin", strings.NewReader(strings.Repeat("x", 4<<20)))
	chunked.ContentLength = -1
	crec := httptest.NewRecorder()
	h.ServeHTTP(crec, chunked.WithContext(context.WithValue(ctx, ctxUserKey, uid)))
	if crec.Code != http.StatusInsufficientStorage {
		t.Fatalf("chunked PUT past the quota = %d, want 507 (%s)", crec.Code, crec.Body.String())
	}

	if rec := put("ok.bin", 900); rec.Code != http.StatusCreated {
		t.Fatalf("PUT inside the quota = %d %s", rec.Code, rec.Body.String())
	}
	rec := put("too-big.bin", 400)
	if rec.Code != http.StatusInsufficientStorage {
		t.Fatalf("PUT past the quota = %d, want 507 (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "insufficient storage") {
		t.Fatalf("507 body must say what happened, got %q", rec.Body.String())
	}

	// macOS junk is discarded rather than stored, so a full quota must not refuse it —
	// Finder reports a failed copy when its sidecar writes error out.
	if rec := put("._junk", 400); rec.Code != http.StatusCreated {
		t.Fatalf("PUT of a discarded junk file = %d %s", rec.Code, rec.Body.String())
	}
}
