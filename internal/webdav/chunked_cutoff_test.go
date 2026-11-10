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

// A chunked PUT declares no length (Finder sends them, and nginx passes them through as
// they are), so the length check cannot tell a cut-off body from a whole one. When the
// body was cut off because it hit the allowance, the bytes that did fit must not be
// published as the file — neither as a new file nor over a good one.
func TestChunkedPutCutOffAtQuota_PublishesNothing(t *testing.T) {
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
	do := func(method, name, body string, length int64) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/dav/"+name, strings.NewReader(body))
		req.ContentLength = length
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req.WithContext(context.WithValue(ctx, ctxUserKey, uid)))
		return rec
	}

	// A new file: nothing may appear under the name.
	if rec := do(http.MethodPut, "new.bin", strings.Repeat("x", 4<<20), -1); rec.Code != http.StatusInsufficientStorage {
		t.Fatalf("chunked PUT past the quota = %d, want 507", rec.Code)
	}
	if rec := do(http.MethodGet, "new.bin", "", 0); rec.Code != http.StatusNotFound {
		t.Errorf("GET new.bin after the refused PUT = %d (%d bytes), want 404", rec.Code, rec.Body.Len())
	}

	// Finder states the size of a chunked body in X-Expected-Entity-Length: a body that
	// arrived short of it is not the file.
	short := httptest.NewRequest(http.MethodPut, "/dav/short.bin", strings.NewReader("12345"))
	short.ContentLength = -1
	short.Header.Set("X-Expected-Entity-Length", "10")
	srec := httptest.NewRecorder()
	h.ServeHTTP(srec, short.WithContext(context.WithValue(ctx, ctxUserKey, uid)))
	if srec.Code < 400 {
		t.Errorf("chunked PUT shorter than X-Expected-Entity-Length = %d, want an error", srec.Code)
	}
	if rec := do(http.MethodGet, "short.bin", "", 0); rec.Code != http.StatusNotFound {
		t.Errorf("GET short.bin = %d, want 404", rec.Code)
	}

	// An existing file: it must keep its content.
	if rec := do(http.MethodPut, "doc.txt", "good content", 12); rec.Code != http.StatusCreated {
		t.Fatalf("PUT doc.txt = %d", rec.Code)
	}
	if rec := do(http.MethodPut, "doc.txt", strings.Repeat("y", 4<<20), -1); rec.Code != http.StatusInsufficientStorage {
		t.Fatalf("chunked overwrite past the quota = %d, want 507", rec.Code)
	}
	if rec := do(http.MethodGet, "doc.txt", "", 0); rec.Body.String() != "good content" {
		t.Errorf("doc.txt after the refused overwrite: %d bytes, want the original 12", rec.Body.Len())
	}
}
