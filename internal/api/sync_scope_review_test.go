package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"discodrive/internal/db"
	"discodrive/internal/storage"
)

type feedEntry struct {
	NodeID  string `json:"node_id"`
	Op      string `json:"op"`
	Path    string `json:"path"`
	IsDir   bool   `json:"is_dir"`
	Deleted bool   `json:"deleted"`
}

// feed reads /sync/changes and returns the entries plus the cursor.
func feed(t *testing.T, h http.Handler, tok string, since int64, scoped bool) ([]feedEntry, int64) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/sync/changes?since="+itoa(since), nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	if scoped {
		req.Header.Set(scopeHeader, "1")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("feed: %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Changes []feedEntry `json:"changes"`
		Cursor  int64       `json:"cursor"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body.Changes, body.Cursor
}

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// last returns the last feed entry about a node (the state a client ends up with).
func last(entries []feedEntry, nodeID string) (feedEntry, bool) {
	var out feedEntry
	found := false
	for _, e := range entries {
		if e.NodeID == nodeID {
			out, found = e, true
		}
	}
	return out, found
}

// A node moved out of the sync folder must reach the scoped daemon as a delete of what
// it has (under the old path); the whole-vault feed keeps reporting the move itself.
func TestScopedFeedReportsMoveOutOfScope(t *testing.T) {
	ctx := context.Background()
	pool, q, svc := bootstrapPairingDB(t)
	root := t.TempDir()
	s := &Server{auth: svc, q: q, files: storage.NewFileService(pool, storage.NewLocalDisk(root)), storageRoot: root}
	tok, user, err := svc.Register(ctx, "moveout@x.test", "password12")
	if err != nil {
		t.Fatal(err)
	}
	uid := db.UUIDString(user.ID)
	dir, err := s.files.EnsureDirByPath(ctx, uid, "sync")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.UpsertSyncSettings(ctx, db.UpsertSyncSettingsParams{UserID: user.ID, Enabled: true, FolderNodeID: dir.ID}); err != nil {
		t.Fatal(err)
	}
	a, err := s.files.PushByPath(ctx, uid, "sync/a.txt", nil, strings.NewReader("a"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.files.PushByPath(ctx, uid, "sync/sub/b.txt", nil, strings.NewReader("b"))
	if err != nil {
		t.Fatal(err)
	}
	sub, err := s.files.NodeByPath(ctx, uid, "/sync/sub")
	if err != nil {
		t.Fatal(err)
	}
	h := svc.Middleware(http.HandlerFunc(s.handleSyncChanges))
	_, cursor := feed(t, h, tok, 0, true)

	aID, bID, subID := db.UUIDString(a.Node.ID), db.UUIDString(b.Node.ID), db.UUIDString(sub.ID)
	if _, err := s.files.Move(ctx, uid, aID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.files.Move(ctx, uid, subID, nil); err != nil {
		t.Fatal(err)
	}

	scoped, next := feed(t, h, tok, cursor, true)
	for _, want := range []struct{ id, path string }{{aID, "a.txt"}, {subID, "sub"}, {bID, "sub/b.txt"}} {
		e, ok := last(scoped, want.id)
		if !ok {
			t.Fatalf("scoped feed says nothing about %s moving out: %+v", want.path, scoped)
		}
		if !e.Deleted || e.Op != "delete" || e.Path != want.path {
			t.Fatalf("moved out %s: got %+v, want a delete at %q", want.path, e, want.path)
		}
	}
	whole, _ := feed(t, h, tok, cursor, false)
	if e, ok := last(whole, aID); !ok || e.Deleted || e.Path != "a.txt" {
		t.Fatalf("whole-vault feed must show the move itself, got %+v", e)
	}
	if e, ok := last(whole, bID); !ok || e.Deleted || e.Path != "sub/b.txt" {
		t.Fatalf("whole-vault feed must show the moved child, got %+v", e)
	}

	// Moving back in is an ordinary change again.
	if _, err := s.files.Move(ctx, uid, aID, ptr(db.UUIDString(dir.ID))); err != nil {
		t.Fatal(err)
	}
	back, _ := feed(t, h, tok, next, true)
	if e, ok := last(back, aID); !ok || e.Deleted || e.Path != "a.txt" {
		t.Fatalf("moved back in: got %+v", e)
	}
	// A rename inside the folder stays an ordinary change.
	if _, err := s.files.Rename(ctx, uid, aID, "renamed.txt"); err != nil {
		t.Fatal(err)
	}
	ren, _ := feed(t, h, tok, next, true)
	if e, ok := last(ren, aID); !ok || e.Deleted || e.Path != "renamed.txt" {
		t.Fatalf("rename inside scope: got %+v", e)
	}
}

func ptr(s string) *string { return &s }

// A deleted sync folder must not turn the daemon's endpoints into 500s, nor (after a
// purge cleared the setting's folder) into whole-vault access: every scoped request gets
// 409 sync_folder_missing until the folder is back or another one is chosen.
func TestMissingSyncFolder(t *testing.T) {
	ctx := context.Background()
	pool, q, svc := bootstrapPairingDB(t)
	root := t.TempDir()
	s := &Server{auth: svc, q: q, files: storage.NewFileService(pool, storage.NewLocalDisk(root)), storageRoot: root}
	tok, user, err := svc.Register(ctx, "missing@x.test", "password12")
	if err != nil {
		t.Fatal(err)
	}
	uid := db.UUIDString(user.ID)
	dir, err := s.files.EnsureDirByPath(ctx, uid, "sync")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.UpsertSyncSettings(ctx, db.UpsertSyncSettingsParams{UserID: user.ID, Enabled: true, FolderNodeID: dir.ID}); err != nil {
		t.Fatal(err)
	}
	did := db.UUIDString(dir.ID)

	call := func(h http.HandlerFunc, method, url, body string, scoped bool) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, url, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		if scoped {
			req.Header.Set(scopeHeader, "1")
		}
		rec := httptest.NewRecorder()
		svc.Middleware(h).ServeHTTP(rec, req)
		return rec
	}
	expectMissing := func(stage string) {
		t.Helper()
		for _, c := range []struct {
			h           http.HandlerFunc
			method, url string
			body        string
		}{
			{s.handleSyncMeta, http.MethodGet, "/sync/meta", ""},
			{s.handleSyncChanges, http.MethodGet, "/sync/changes?since=0", ""},
			{s.handleSyncPutFile, http.MethodPut, "/sync/file?path=x.txt", "data"},
			{s.handleSyncMkdir, http.MethodPost, "/sync/dir", `{"path":"d"}`},
			{s.handleSyncDelete, http.MethodDelete, "/sync/file?path=x.txt", ""},
		} {
			rec := call(c.h, c.method, c.url, c.body, true)
			var m map[string]any
			_ = json.Unmarshal(rec.Body.Bytes(), &m)
			if rec.Code != http.StatusConflict || m["code"] != "sync_folder_missing" {
				t.Fatalf("%s: %s %s = %d %s, want 409 sync_folder_missing", stage, c.method, c.url, rec.Code, rec.Body.String())
			}
		}
		// Clients that do not ask for the scope keep working.
		if rec := call(s.handleSyncMeta, http.MethodGet, "/sync/meta", "", false); rec.Code != http.StatusOK {
			t.Fatalf("%s: unscoped /sync/meta = %d", stage, rec.Code)
		}
		if rec := call(s.handleSyncChanges, http.MethodGet, "/sync/changes?since=0", "", false); rec.Code != http.StatusOK {
			t.Fatalf("%s: unscoped /sync/changes = %d", stage, rec.Code)
		}
		rec := call(s.handleGetSyncSettings, http.MethodGet, "/me/sync", "", false)
		var m map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &m)
		if m["folder_missing"] != true {
			t.Fatalf("%s: GET /me/sync = %s, want folder_missing", stage, rec.Body.String())
		}
		if _, err := os.Stat(filepath.Join(root, uid, "x.txt")); err == nil {
			t.Fatalf("%s: a scoped push landed in the vault root", stage)
		}
	}

	if err := s.files.Delete(ctx, uid, did); err != nil {
		t.Fatal(err)
	}
	expectMissing("trashed")

	// Restoring the folder resumes the scope as it was.
	if _, err := s.files.Undelete(ctx, uid, did); err != nil {
		t.Fatal(err)
	}
	if rec := call(s.handleSyncChanges, http.MethodGet, "/sync/changes?since=0", "", true); rec.Code != http.StatusOK {
		t.Fatalf("after restore: %d %s", rec.Code, rec.Body.String())
	}

	// Purged: the foreign key clears folder_node_id but leaves the scope enabled.
	if err := s.files.Delete(ctx, uid, did); err != nil {
		t.Fatal(err)
	}
	if err := s.files.Purge(ctx, uid, did); err != nil {
		t.Fatal(err)
	}
	expectMissing("purged")
}
