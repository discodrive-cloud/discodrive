package saved

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"discodrive/internal/db"
	"discodrive/internal/quota"
	"discodrive/internal/storage"
)

// withQuota wires the service the way main does: a per-user quota, the shared checker,
// and the file service as publisher.
func withQuota(t *testing.T, svc *Service, pool *pgxpool.Pool, q *db.Queries, uid pgtype.UUID, root string, limit int64) *storage.FileService {
	t.Helper()
	if _, err := q.UpdateUser(context.Background(), db.UpdateUserParams{ID: uid, StorageQuota: pgtype.Int8{Int64: limit, Valid: true}, Role: "user"}); err != nil {
		t.Fatalf("set quota: %v", err)
	}
	fs := storage.NewFileService(pool, storage.NewLocalDisk(root))
	checker := quota.New(q, 0)
	fs.SetQuota(checker)
	svc.SetQuota(checker)
	svc.SetFiles(fs)
	return fs
}

// waitFinished polls until the item is done or failed.
func waitFinished(t *testing.T, q *db.Queries, id, uid pgtype.UUID) db.SavedItem {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		it, err := q.GetSavedItemForUser(context.Background(), db.GetSavedItemForUserParams{ID: id, UserID: uid})
		if err == nil && (it.Status == StatusDone || it.Status == StatusError) {
			return it
		}
		if time.Now().After(deadline) {
			t.Fatalf("item %s still %q", db.UUIDString(id), it.Status)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// A finished download is a node at once: visible in Files and counted in the quota
// without waiting for a rescan.
func TestDownloadBecomesNodeAndCountsTowardQuota(t *testing.T) {
	payload := strings.Repeat("x", 100_000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Disposition", `attachment; filename="report.bin"`)
		_, _ = w.Write([]byte(payload))
	}))
	defer srv.Close()

	svc, pool, q, uid, root := bootstrap(t, 0)
	fs := withQuota(t, svc, pool, q, uid, root, 10<<20)
	item, err := svc.Create(context.Background(), uid, srv.URL+"/dl", KindDownload, "", "", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	waitStatus(t, q, item.ID, uid, StatusDone)

	node, err := fs.NodeByPath(context.Background(), db.UUIDString(uid), "/Downloads/report.bin")
	if err != nil {
		t.Fatalf("download has no node: %v", err)
	}
	if node.Size.Int64 != int64(len(payload)) {
		t.Fatalf("node size = %d", node.Size.Int64)
	}
	used, err := q.UserStorageUsage(context.Background(), uid)
	if err != nil {
		t.Fatal(err)
	}
	if used != int64(len(payload)) {
		t.Fatalf("used = %d, want %d (counted once, as a node)", used, len(payload))
	}
}

// Parallel downloads of one user must see each other's bytes: each one alone fits the
// quota, together they do not, so they cannot both finish. The allowance is re-read
// once per quota.ReserveBlock (4 MiB), so the files are several blocks long.
func TestParallelDownloadsShareTheQuota(t *testing.T) {
	const (
		size  = 12 << 20
		limit = 16 << 20
		parts = 24
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(size))
		chunk := []byte(strings.Repeat("y", size/parts))
		for range parts { // ~2.4s per download: several progress writes and re-checks
			_, _ = w.Write(chunk)
			w.(http.Flusher).Flush()
			time.Sleep(100 * time.Millisecond)
		}
	}))
	defer srv.Close()

	svc, pool, q, uid, root := bootstrap(t, 0)
	withQuota(t, svc, pool, q, uid, root, limit)
	a, err := svc.Create(context.Background(), uid, srv.URL+"/a.bin", KindDownload, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := svc.Create(context.Background(), uid, srv.URL+"/b.bin", KindDownload, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	ra, rb := waitFinished(t, q, a.ID, uid), waitFinished(t, q, b.ID, uid)
	if ra.Status == StatusDone && rb.Status == StatusDone {
		t.Fatalf("both %d-byte downloads finished under a %d-byte quota", size, limit)
	}
	// Whatever failed must have failed on the quota, not on something unrelated (the
	// two downloads also race to create the Downloads folder).
	for _, it := range []db.SavedItem{ra, rb} {
		if it.Status == StatusError && !strings.Contains(strings.ToLower(it.ErrorMsg), "quota") && !strings.Contains(strings.ToLower(it.ErrorMsg), "storage") {
			t.Fatalf("download failed for another reason: %q", it.ErrorMsg)
		}
	}
	used, err := q.UserStorageUsage(context.Background(), uid)
	if err != nil {
		t.Fatal(err)
	}
	if used > limit {
		t.Fatalf("used %d bytes, over the quota", used)
	}
}
