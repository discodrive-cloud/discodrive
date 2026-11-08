package webdav

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"discodrive/internal/db"
	"discodrive/internal/storage"
)

// x/net/webdav creates the destination before listing the source, and the source lists
// its children lazily, so a COPY of a folder into its own subfolder found the new folder
// inside itself and recursed ~1000 levels deep. Such a COPY must be refused outright.
func TestCopyIntoOwnSubtreeIsRefused(t *testing.T) {
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
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	q := db.New(pool)
	tenant, _ := q.CreateTenant(ctx, "t")
	user, err := q.CreateUser(ctx, db.CreateUserParams{TenantID: tenant.ID, Email: "u@x", PasswordHash: "x", Role: "user"})
	if err != nil {
		t.Fatal(err)
	}
	uid := db.UUIDString(user.ID)
	h := Handler(storage.NewFileService(pool, storage.NewLocalDisk(t.TempDir())), "/dav")
	do := func(method, target, body string, hdr map[string]string) int {
		t.Helper()
		req := httptest.NewRequest(method, target, strings.NewReader(body))
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req.WithContext(context.WithValue(ctx, ctxUserKey, uid)))
		return rec.Code
	}
	do("MKCOL", "/dav/A", "", nil)
	do("MKCOL", "/dav/A/0dir", "", nil)
	do(http.MethodPut, "/dav/A/0dir/f.txt", "hello", nil)

	for _, dst := range []string{"/dav/A/B", "/dav/A/0dir/B", "/dav/A"} {
		code := do("COPY", "/dav/A", "", map[string]string{"Destination": dst, "Depth": "infinity"})
		if code < 400 {
			t.Fatalf("COPY /dav/A -> %s = %d, want a refusal", dst, code)
		}
	}
	if code := do("PROPFIND", "/dav/A/B", "", map[string]string{"Depth": "0"}); code != http.StatusNotFound {
		t.Fatalf("PROPFIND /dav/A/B = %d after a refused copy, want 404", code)
	}
	// a copy next to the source still works, and so does one into a name that only
	// shares a prefix with the source
	if code := do("COPY", "/dav/A", "", map[string]string{"Destination": "/dav/AB", "Depth": "infinity"}); code != http.StatusCreated {
		t.Fatalf("COPY /dav/A -> /dav/AB = %d, want 201", code)
	}
}
