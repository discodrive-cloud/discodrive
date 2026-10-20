package webdav_test

import (
	"bytes"
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/net/webdav"

	"github.com/jackc/pgx/v5/pgxpool"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"discodrive/internal/db"
	"discodrive/internal/storage"
	kfdav "discodrive/internal/webdav"
)

// setupWithRoot is like setup() but also returns the on-disk storage root, so a test can
// simulate a node whose backing content file is missing.
func setupWithRoot(t *testing.T) (kfdav.FileSystem, *storage.FileService, string, string) {
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
		t.Fatalf("migrations: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	q := db.New(pool)
	tenant, _ := q.CreateTenant(ctx, "t")
	user, _ := q.CreateUser(ctx, db.CreateUserParams{TenantID: tenant.ID, Email: "u@x", PasswordHash: "x", Role: "user"})
	uid := db.UUIDString(user.ID)
	root := t.TempDir()
	fs := storage.NewFileService(pool, storage.NewLocalDisk(root))
	return kfdav.NewFileSystem(fs, uid), fs, uid, root
}

// Reproduces the "superfluous response.WriteHeader" the user saw when Finder enumerated the
// WebDAV root. Root cause: nodeInfo does not implement webdav.ContentTyper, so during PROPFIND
// the library OPENS every regular file to sniff its Content-Type. If any file's content can't
// be opened, the directory walk errors AFTER the multistatus body was already started, so the
// handler calls WriteHeader(500) a second time — and the whole listing breaks for Finder.
func TestPropfindDoesNotOpenFileContents(t *testing.T) {
	ctx := context.Background()
	wfs, fs, uid, root := setupWithRoot(t)

	// A healthy file plus one whose backing content is missing on disk.
	if _, err := fs.Push(ctx, uid, nil, "good.txt", nil, "init", strings.NewReader("hello")); err != nil {
		t.Fatalf("push good: %v", err)
	}
	if _, err := fs.Push(ctx, uid, nil, "bad.txt", nil, "init", strings.NewReader("data")); err != nil {
		t.Fatalf("push bad: %v", err)
	}
	bad, err := fs.NodeByPath(ctx, uid, "/bad.txt")
	if err != nil {
		t.Fatalf("lookup bad: %v", err)
	}
	if err := os.Remove(filepath.Join(root, bad.DiskPath.String)); err != nil {
		t.Fatalf("simulate missing content: %v", err)
	}

	h := &webdav.Handler{Prefix: "/dav", FileSystem: wfs, LockSystem: webdav.NewMemLS()}
	var logBuf bytes.Buffer
	srv := httptest.NewUnstartedServer(h)
	srv.Config.ErrorLog = log.New(&logBuf, "", 0)
	srv.Start()
	defer srv.Close()

	body := `<?xml version="1.0" encoding="utf-8"?><propfind xmlns="DAV:"><allprop/></propfind>`
	req, _ := http.NewRequest("PROPFIND", srv.URL+"/dav/", strings.NewReader(body))
	req.Header.Set("Depth", "1")
	req.Header.Set("Content-Type", "application/xml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PROPFIND: %v", err)
	}
	defer resp.Body.Close()
	respBody := new(strings.Builder)
	_, _ = io.Copy(respBody, resp.Body)

	if got := logBuf.String(); strings.Contains(got, "superfluous") {
		t.Fatalf("server logged a superfluous WriteHeader during PROPFIND:\n%s", got)
	}
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("PROPFIND status = %d, want 207; body:\n%s", resp.StatusCode, respBody.String())
	}
	// Enumeration must still include both entries (listing a folder must not require reading files).
	for _, want := range []string{"good.txt", "bad.txt"} {
		if !strings.Contains(respBody.String(), want) {
			t.Errorf("PROPFIND listing missing %q:\n%s", want, respBody.String())
		}
	}
}

// A child that the listing returns but Stat() reports as non-existent (here: a macOS junk
// node) must NOT abort the whole directory. This is the failure seen in production: one such
// child made PROPFIND emit "superfluous WriteHeader" and Finder showed zero files.
func TestPropfindSkipsUnlistableChild(t *testing.T) {
	ctx := context.Background()
	wfs, fs, uid, _ := setupWithRoot(t)

	if _, err := fs.Push(ctx, uid, nil, "real.txt", nil, "init", strings.NewReader("hi")); err != nil {
		t.Fatalf("push real: %v", err)
	}
	// A .DS_Store node stored at the root (e.g. created by a non-WebDAV path): the WebDAV FS
	// treats it as non-existent, which previously aborted the listing.
	if _, err := fs.Push(ctx, uid, nil, ".DS_Store", nil, "init", strings.NewReader("junk")); err != nil {
		t.Fatalf("push junk: %v", err)
	}

	h := &webdav.Handler{Prefix: "/dav", FileSystem: wfs, LockSystem: webdav.NewMemLS()}
	var logBuf bytes.Buffer
	srv := httptest.NewUnstartedServer(h)
	srv.Config.ErrorLog = log.New(&logBuf, "", 0)
	srv.Start()
	defer srv.Close()

	body := `<?xml version="1.0" encoding="utf-8"?><propfind xmlns="DAV:"><allprop/></propfind>`
	req, _ := http.NewRequest("PROPFIND", srv.URL+"/dav/", strings.NewReader(body))
	req.Header.Set("Depth", "1")
	req.Header.Set("Content-Type", "application/xml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PROPFIND: %v", err)
	}
	defer resp.Body.Close()
	respBody := new(strings.Builder)
	_, _ = io.Copy(respBody, resp.Body)

	if got := logBuf.String(); strings.Contains(got, "superfluous") {
		t.Fatalf("superfluous WriteHeader (listing aborted):\n%s", got)
	}
	if resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("PROPFIND status = %d, want 207; body:\n%s", resp.StatusCode, respBody.String())
	}
	if !strings.Contains(respBody.String(), "real.txt") {
		t.Errorf("listing must include real.txt:\n%s", respBody.String())
	}
	if strings.Contains(respBody.String(), ".DS_Store") {
		t.Errorf("macOS junk must not be exposed over WebDAV:\n%s", respBody.String())
	}
}

// Windows Explorer opens a share as /dav, without the trailing slash; the root must answer
// with the same listing instead of 404.
func TestPropfindRootWithoutSlash(t *testing.T) {
	ctx := context.Background()
	wfs, fs, uid := setup(t)
	if _, err := fs.Push(ctx, uid, nil, "hello.txt", nil, "init", strings.NewReader("hi")); err != nil {
		t.Fatalf("push: %v", err)
	}
	srv := httptest.NewServer(&webdav.Handler{Prefix: "/dav", FileSystem: wfs, LockSystem: webdav.NewMemLS()})
	defer srv.Close()

	req, _ := http.NewRequest("PROPFIND", srv.URL+"/dav", strings.NewReader(`<?xml version="1.0"?><propfind xmlns="DAV:"><allprop/></propfind>`))
	req.Header.Set("Depth", "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PROPFIND: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusMultiStatus || !strings.Contains(string(body), "hello.txt") {
		t.Fatalf("PROPFIND /dav = %d:\n%s", resp.StatusCode, body)
	}
}
