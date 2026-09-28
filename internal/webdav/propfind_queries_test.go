package webdav

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"golang.org/x/net/webdav"

	"discodrive/internal/db"
	"discodrive/internal/storage"
)

func newCountingFS(t *testing.T) (*fsImpl, *storage.FileService, string) {
	t.Helper()
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
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	q := db.New(pool)
	tenant, _ := q.CreateTenant(ctx, "t")
	user, _ := q.CreateUser(ctx, db.CreateUserParams{TenantID: tenant.ID, Email: "u@x", PasswordHash: "x", Role: "user"})
	uid := db.UUIDString(user.ID)
	svc := storage.NewFileService(pool, storage.NewLocalDisk(t.TempDir()))
	return NewFileSystem(svc, uid).(*fsImpl), svc, uid
}

// propfind runs PROPFIND Depth:1 through the library handler, with the per-request
// memo installed the way handler.go does it.
func propfind(t *testing.T, fsys *fsImpl, target string) string {
	t.Helper()
	h := &webdav.Handler{Prefix: "/dav", FileSystem: fsys, LockSystem: webdav.NewMemLS()}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r.WithContext(withNodeMemo(r.Context())))
	}))
	defer srv.Close()
	body := `<?xml version="1.0" encoding="utf-8"?><propfind xmlns="DAV:"><allprop/></propfind>`
	req, _ := http.NewRequest("PROPFIND", srv.URL+"/dav"+target, strings.NewReader(body))
	req.Header.Set("Depth", "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("PROPFIND %s: %d\n%s", target, resp.StatusCode, out)
	}
	return string(out)
}

// PROPFIND Depth:1 used to look every child up in the database about three times
// (Stat, then OpenFile for the property names and again for the values) and to list
// every child folder's own contents: 5 000 files took ~40 s on a Pi. The number of
// database calls must not grow with the size of the folder.
func TestPropfindQueriesDoNotGrowWithFolderSize(t *testing.T) {
	fsys, svc, uid := newCountingFS(t)
	ctx := context.Background()
	folder, err := svc.CreateFolder(ctx, uid, nil, "big")
	if err != nil {
		t.Fatal(err)
	}
	fid := db.UUIDString(folder.ID)
	for i := range 60 {
		if _, err := svc.Push(ctx, uid, &fid, fmt.Sprintf("f%02d.txt", i), nil, "", strings.NewReader("x")); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 5 {
		sub, err := svc.CreateFolder(ctx, uid, &fid, fmt.Sprintf("sub%d", i))
		if err != nil {
			t.Fatal(err)
		}
		sid := db.UUIDString(sub.ID)
		if _, err := svc.Push(ctx, uid, &sid, "inner.txt", nil, "", strings.NewReader("y")); err != nil {
			t.Fatal(err)
		}
	}

	fsys.dbCalls.Store(0)
	out := propfind(t, fsys, "/big/")
	for _, want := range []string{"f00.txt", "f59.txt", "sub4"} {
		if !strings.Contains(out, want) {
			t.Fatalf("listing misses %q", want)
		}
	}
	if strings.Contains(out, "inner.txt") {
		t.Fatal("Depth:1 listed a grandchild")
	}
	if n := fsys.dbCalls.Load(); n > 4 {
		t.Fatalf("PROPFIND of 65 entries made %d database calls, want a constant handful", n)
	}
}

// Within one request a change must not be answered from what was looked up before it.
func TestNodeMemoIsDroppedOnChange(t *testing.T) {
	fsys, svc, uid := newCountingFS(t)
	ctx := withNodeMemo(context.Background())
	if _, err := svc.Push(context.Background(), uid, nil, "a.txt", nil, "", strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	if _, err := fsys.Stat(ctx, "/a.txt"); err != nil {
		t.Fatal(err)
	}
	if err := fsys.Rename(ctx, "/a.txt", "/b.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := fsys.Stat(ctx, "/a.txt"); err == nil {
		t.Fatal("renamed path still found: answered from the memo")
	}
	if _, err := fsys.Stat(ctx, "/b.txt"); err != nil {
		t.Fatalf("new path: %v", err)
	}
}
