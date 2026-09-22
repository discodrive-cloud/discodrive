package saved

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"discodrive/internal/db"
	"discodrive/internal/secret"
	"discodrive/internal/storage"
)

// bootstrap spins up Postgres, runs migrations, and returns a Service writing
// to a temp storage root, with the SSRF guard disabled so tests can reach
// httptest servers on 127.0.0.1.
func bootstrap(t *testing.T, maxDownloadMB int) (*Service, *pgxpool.Pool, *db.Queries, pgtype.UUID, string) {
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
		t.Fatalf("migrations: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool: %v", err)
	}
	t.Cleanup(pool.Close)
	q := db.New(pool)
	tenant, _ := q.CreateTenant(ctx, "t")
	u, err := q.CreateUser(ctx, db.CreateUserParams{TenantID: tenant.ID, Email: "u@x", PasswordHash: "x", Role: "user"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	root := t.TempDir()
	svc := NewService(q, storage.NewLocalDisk(root), maxDownloadMB)
	cipher, err := secret.New("0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	svc.SetCipher(cipher)
	svc.Client = &http.Client{}
	svc.Validate = func(string) error { return nil }
	return svc, pool, q, u.ID, root
}

// waitStatus polls until the item reaches want (or fails the test after 15s).
func waitStatus(t *testing.T, q *db.Queries, id, uid pgtype.UUID, want string) db.SavedItem {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		it, err := q.GetSavedItemForUser(context.Background(), db.GetSavedItemForUserParams{ID: id, UserID: uid})
		if err == nil && it.Status == want {
			return it
		}
		if time.Now().After(deadline) {
			t.Fatalf("item %s did not reach status %q (last: %q, err: %q)", db.UUIDString(id), want, it.Status, it.ErrorMsg)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestDownloadHappyPath(t *testing.T) {
	payload := strings.Repeat("x", 100_000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Disposition", `attachment; filename="report v1.bin"`)
		_, _ = w.Write([]byte(payload))
	}))
	defer srv.Close()

	svc, _, q, uid, root := bootstrap(t, 0)
	item, err := svc.Create(context.Background(), uid, srv.URL+"/dl", KindDownload, "", "", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	done := waitStatus(t, q, item.ID, uid, StatusDone)

	wantRel := db.UUIDString(uid) + "/Downloads/report v1.bin"
	if done.ContentPath.String != wantRel {
		t.Fatalf("content_path = %q, want %q", done.ContentPath.String, wantRel)
	}
	b, err := os.ReadFile(filepath.Join(root, wantRel))
	if err != nil {
		t.Fatalf("read downloaded file: %v", err)
	}
	if len(b) != len(payload) {
		t.Fatalf("file size = %d, want %d", len(b), len(payload))
	}
	if !done.SizeBytes.Valid || done.SizeBytes.Int64 != int64(len(payload)) {
		t.Fatalf("size_bytes = %+v, want %d", done.SizeBytes, len(payload))
	}
	if done.Title != "report v1.bin" {
		t.Fatalf("title = %q, want the file name", done.Title)
	}
	// Staging dir must not accumulate leftovers.
	if leftovers, _ := filepath.Glob(filepath.Join(root, ".tmp", "saved-*")); len(leftovers) != 0 {
		t.Fatalf("staging leftovers: %v", leftovers)
	}
}

func TestDownloadNameFromURLAndCollision(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("data-" + r.URL.Path))
	}))
	defer srv.Close()

	svc, _, q, uid, root := bootstrap(t, 0)
	first, _ := svc.Create(context.Background(), uid, srv.URL+"/files/image.iso", KindDownload, "", "", "")
	waitStatus(t, q, first.ID, uid, StatusDone)
	// A different URL yielding the same file name must get a -2 suffix.
	second, _ := svc.Create(context.Background(), uid, srv.URL+"/other/image.iso", KindDownload, "", "", "")
	done2 := waitStatus(t, q, second.ID, uid, StatusDone)

	if _, err := os.Stat(filepath.Join(root, db.UUIDString(uid), "Downloads", "image.iso")); err != nil {
		t.Fatalf("first file missing: %v", err)
	}
	if want := db.UUIDString(uid) + "/Downloads/image-2.iso"; done2.ContentPath.String != want {
		t.Fatalf("second content_path = %q, want %q", done2.ContentPath.String, want)
	}
}

func TestDownloadSizeLimitContentLength(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(10<<20))
		_, _ = w.Write(make([]byte, 1024))
	}))
	defer srv.Close()

	svc, _, q, uid, _ := bootstrap(t, 1) // 1 MiB cap
	item, _ := svc.Create(context.Background(), uid, srv.URL+"/big.bin", KindDownload, "", "", "")
	errored := waitStatus(t, q, item.ID, uid, StatusError)
	// The user reads this message: concrete sizes and the knob to turn, with
	// no internal package prefixes.
	for _, want := range []string{"10.0 MB", "1.0 MB", "SAVED_MAX_DOWNLOAD_MB"} {
		if !strings.Contains(errored.ErrorMsg, want) {
			t.Fatalf("error_msg = %q, want it to mention %q", errored.ErrorMsg, want)
		}
	}
	if strings.Contains(errored.ErrorMsg, "saved:") {
		t.Fatalf("error_msg = %q — internal package prefix must not leak", errored.ErrorMsg)
	}
}

func TestDownloadSizeLimitStreaming(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Chunked (no Content-Length): the limit must trip on the byte counter.
		fl, _ := w.(http.Flusher)
		chunk := make([]byte, 256<<10)
		for i := 0; i < 8; i++ { // 2 MiB total
			if _, err := w.Write(chunk); err != nil {
				return
			}
			if fl != nil {
				fl.Flush()
			}
		}
	}))
	defer srv.Close()

	svc, _, q, uid, root := bootstrap(t, 1) // 1 MiB cap
	item, _ := svc.Create(context.Background(), uid, srv.URL+"/stream.bin", KindDownload, "", "", "")
	errored := waitStatus(t, q, item.ID, uid, StatusError)
	if !strings.Contains(errored.ErrorMsg, "limit is") {
		t.Fatalf("error_msg = %q, want a size-limit error", errored.ErrorMsg)
	}
	if leftovers, _ := filepath.Glob(filepath.Join(root, ".tmp", "saved-*")); len(leftovers) != 0 {
		t.Fatalf("staging leftovers after failed download: %v", leftovers)
	}
}

func TestDeleteMidDownloadAbortsAndCleansUp(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fl, _ := w.(http.Flusher)
		chunk := make([]byte, 64<<10)
		for i := 0; i < 100; i++ { // slow stream: ~10s unless aborted
			select {
			case <-release:
				return
			case <-r.Context().Done():
				return
			case <-time.After(100 * time.Millisecond):
			}
			if _, err := w.Write(chunk); err != nil {
				return
			}
			if fl != nil {
				fl.Flush()
			}
		}
	}))
	defer srv.Close()
	defer close(release)

	svc, pool, q, uid, root := bootstrap(t, 0)
	item, _ := svc.Create(context.Background(), uid, srv.URL+"/slow.bin", KindDownload, "", "", "")

	// Wait until the download reports progress, then delete the row (what the
	// DELETE endpoint does): the goroutine must notice and discard the staging file.
	deadline := time.Now().Add(15 * time.Second)
	for {
		it, err := q.GetSavedItemForUser(context.Background(), db.GetSavedItemForUserParams{ID: item.ID, UserID: uid})
		if err == nil && it.Status == StatusProcessing && it.BytesDone > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("download never reported progress")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, err := pool.Exec(context.Background(), "DELETE FROM saved_items WHERE id=$1", item.ID); err != nil {
		t.Fatalf("delete row: %v", err)
	}

	// Within a few progress ticks the goroutine must abort and clean up.
	deadline = time.Now().Add(15 * time.Second)
	for {
		leftovers, _ := filepath.Glob(filepath.Join(root, ".tmp", "saved-*"))
		if len(leftovers) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("staging file still present after delete: %v", leftovers)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if entries, _ := os.ReadDir(filepath.Join(root, db.UUIDString(uid), "Downloads")); len(entries) != 0 {
		t.Fatalf("downloads dir must stay empty, got %v", entries)
	}
}

// TestDownloadWithCookie: the browser session cookie reaches the request (for
// sites behind a login) and is wiped from the row once the item is done.
func TestDownloadWithCookie(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "session=secret42" {
			http.Error(w, "login required", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Disposition", `attachment; filename="private.bin"`)
		_, _ = w.Write([]byte("member-only payload"))
	}))
	defer srv.Close()

	svc, pool, q, uid, root := bootstrap(t, 0)
	svc.Client = srv.Client()

	// Without the cookie: an honest error from the site.
	noCookie, _ := svc.Create(context.Background(), uid, srv.URL+"/private", KindDownload, "", "", "")
	errored := waitStatus(t, q, noCookie.ID, uid, StatusError)
	if !strings.Contains(errored.ErrorMsg, "403") {
		t.Fatalf("without cookie: error_msg = %q, want 403", errored.ErrorMsg)
	}

	// With the cookie: the file downloads.
	withCookie, err := svc.Create(context.Background(), uid, srv.URL+"/private2", KindDownload, "", "", "session=secret42")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	done := waitStatus(t, q, withCookie.ID, uid, StatusDone)
	b, err := os.ReadFile(filepath.Join(root, done.ContentPath.String))
	if err != nil || string(b) != "member-only payload" {
		t.Fatalf("file: %v %q", err, b)
	}
	// The cookie is transport, not storage: the column is cleared once done.
	var stored *string
	if err := pool.QueryRow(context.Background(), "SELECT cookie_header FROM saved_items WHERE id=$1", withCookie.ID).Scan(&stored); err != nil {
		t.Fatalf("select cookie_header: %v", err)
	}
	if stored != nil {
		t.Fatalf("cookie_header must be cleared after done, got %q", *stored)
	}
}

// TestResubmitErroredRetries: resubmitting a failed link restarts it (otherwise
// the user would keep getting the old error from a row that lives until the
// cleanup), while a finished one is left as it is.
func TestResubmitErroredRetries(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if fail.Load() {
			http.Error(w, "boom", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("payload"))
	}))
	defer srv.Close()

	svc, _, q, uid, _ := bootstrap(t, 0)
	ctx := context.Background()

	item, err := svc.Create(ctx, uid, srv.URL+"/f.bin", KindDownload, "", "", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	waitStatus(t, q, item.ID, uid, StatusError)

	// The site is back up: resubmitting the same link must fetch it again.
	fail.Store(false)
	again, err := svc.Create(ctx, uid, srv.URL+"/f.bin", KindDownload, "", "", "")
	if err != nil {
		t.Fatalf("resubmit: %v", err)
	}
	if again.ID != item.ID {
		t.Fatal("resubmit must reuse the same row")
	}
	if again.Status == StatusError {
		t.Fatalf("resubmit must reset the errored row, got status %q", again.Status)
	}
	done := waitStatus(t, q, item.ID, uid, StatusDone)
	if done.ErrorMsg != "" {
		t.Fatalf("error_msg must be cleared, got %q", done.ErrorMsg)
	}

	// Resubmitting a finished item is a no-op, with no extra trip to the site.
	before := hits.Load()
	if _, err := svc.Create(ctx, uid, srv.URL+"/f.bin", KindDownload, "", "", ""); err != nil {
		t.Fatalf("resubmit done: %v", err)
	}
	time.Sleep(500 * time.Millisecond)
	if hits.Load() != before {
		t.Fatalf("re-saving a done item must not refetch (hits %d → %d)", before, hits.Load())
	}
}

func TestDownloadFilename(t *testing.T) {
	cases := []struct {
		disposition, url, want string
	}{
		{`attachment; filename="report.pdf"`, "https://x/y", "report.pdf"},
		{"", "https://example.com/files/ubuntu-24.04.iso?mirror=1", "ubuntu-24.04.iso"},
		{"", "https://example.com/files/with%20space.zip", "with space.zip"},
		{"", "https://example.com/", "download"},
		{`attachment; filename="../../etc/passwd"`, "https://x/y", ""}, // checked below: just must be traversal-free
		{`attachment; filename=".hidden"`, "https://x/y", "hidden"},
	}
	for _, c := range cases {
		got := downloadFilename(c.disposition, c.url)
		if strings.ContainsAny(got, `/\`) || strings.HasPrefix(got, ".") || got == "" {
			t.Errorf("downloadFilename(%q, %q) = %q — unsafe or empty", c.disposition, c.url, got)
		}
		if c.want != "" && got != c.want {
			t.Errorf("downloadFilename(%q, %q) = %q, want %q", c.disposition, c.url, got, c.want)
		}
	}
	long := strings.Repeat("я", 300) + ".bin"
	if got := sanitizeFilename(long); len(got) > 150 || !strings.HasSuffix(got, ".bin") {
		t.Errorf("sanitizeFilename(long) = %q (len %d) — must be capped and keep the extension", got, len(got))
	}
}
