package webdav

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"discodrive/internal/db"
	"discodrive/internal/storage"
)

// MOVE with "Overwrite: T" replaces the destination and moves the source as one step:
// when the move cannot happen, the destination must still be there (x/net/webdav used
// to trash it first and then fail the rename), and a move plus rename must not stop
// halfway on a name that exists only in between.
func TestMoveOverwriteIsAtomic(t *testing.T) {
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
	svc := storage.NewFileService(pool, storage.NewLocalDisk(t.TempDir()))
	h := Handler(svc, "/dav")

	do := func(method, target string, body string, hdr map[string]string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, target, strings.NewReader(body))
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req.WithContext(context.WithValue(ctx, ctxUserKey, uid)))
		return rec
	}
	read := func(p string) string {
		t.Helper()
		rec := do(http.MethodGet, "/dav"+p, "", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d", p, rec.Code)
		}
		b, _ := io.ReadAll(rec.Body)
		return string(b)
	}
	for _, dir := range []string{"/x", "/y", "/p"} {
		if rec := do("MKCOL", "/dav"+dir, "", nil); rec.Code != http.StatusCreated {
			t.Fatalf("MKCOL %s = %d", dir, rec.Code)
		}
	}
	for p, body := range map[string]string{"/x/a.txt": "source", "/y/a.txt": "bystander", "/y/b.txt": "target", "/p/c.txt": "inside"} {
		if rec := do(http.MethodPut, "/dav"+p, body, nil); rec.Code != http.StatusCreated {
			t.Fatalf("PUT %s = %d", p, rec.Code)
		}
	}

	// Move and rename at once, over an existing file, into a folder where the source's
	// old name is taken: legal, since the source ends up as b.txt.
	rec := do("MOVE", "/dav/x/a.txt", "", map[string]string{"Destination": "/dav/y/b.txt", "Overwrite": "T"})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("MOVE over b.txt = %d %s", rec.Code, rec.Body.String())
	}
	if got := read("/y/b.txt"); got != "source" {
		t.Fatalf("y/b.txt = %q, want the moved content", got)
	}
	if got := read("/y/a.txt"); got != "bystander" {
		t.Fatalf("y/a.txt = %q", got)
	}
	if rec := do("PROPFIND", "/dav/x/a.txt", "", map[string]string{"Depth": "0"}); rec.Code != http.StatusNotFound {
		t.Fatalf("x/a.txt still there: %d", rec.Code)
	}
	trash, err := svc.Trash(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if len(trash) != 1 || trash[0].Name != "b.txt" {
		t.Fatalf("the replaced b.txt should be in the trash, trash has %d", len(trash))
	}

	// A move that cannot happen: the destination is the source's own folder. The folder
	// must not be trashed on the way to the failure.
	rec = do("MOVE", "/dav/p/c.txt", "", map[string]string{"Destination": "/dav/p", "Overwrite": "T"})
	if rec.Code < 400 {
		t.Fatalf("MOVE into its own replaced parent = %d, want a failure", rec.Code)
	}
	if got := read("/p/c.txt"); got != "inside" {
		t.Fatalf("p/c.txt after the failed MOVE = %q", got)
	}
	if trash, _ := svc.Trash(ctx, uid); len(trash) != 1 {
		t.Fatalf("a failed MOVE trashed something: trash has %d", len(trash))
	}
}
