package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"discodrive/internal/db"
	"discodrive/internal/storage"
)

// A folder purged from the trash reaches a client whose cursor predates its deletion
// as deletes on the wire: whole-vault paths without the scope header, paths relative to
// the sync folder with it.
func TestSyncChangesReportPurgedNodes(t *testing.T) {
	ctx := context.Background()
	pool, q, svc := bootstrapPairingDB(t)
	root := t.TempDir()
	s := &Server{
		auth: svc, q: q, storageRoot: root,
		files: storage.NewFileService(pool, storage.NewLocalDisk(root)),
	}
	tok, user, err := svc.Register(ctx, "purged@x.test", "password12")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	uid := db.UUIDString(user.ID)
	sync, err := s.files.EnsureDirByPath(ctx, uid, "sync")
	if err != nil {
		t.Fatalf("sync dir: %v", err)
	}
	if _, err := q.UpsertSyncSettings(ctx, db.UpsertSyncSettingsParams{
		UserID: user.ID, Enabled: true, FolderNodeID: sync.ID,
	}); err != nil {
		t.Fatalf("sync settings: %v", err)
	}
	sid := db.UUIDString(sync.ID)
	docs, err := s.files.CreateFolder(ctx, uid, &sid, "docs")
	if err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	did := db.UUIDString(docs.ID)
	f, err := s.files.Push(ctx, uid, &did, "f.txt", nil, "i", strings.NewReader("hello"))
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	fid := db.UUIDString(f.Node.ID)

	h := svc.Middleware(http.HandlerFunc(s.handleSyncChanges))
	cursor := int64(changesReq(t, h, "/sync/changes?since=0", tok, false)["cursor"].(float64))
	scopedCursor := int64(changesReq(t, h, "/sync/changes?since=0", tok, true)["cursor"].(float64))

	if err := s.files.Delete(ctx, uid, did); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := s.files.Purge(ctx, uid, did); err != nil {
		t.Fatalf("purge: %v", err)
	}

	byID := func(m map[string]any) map[string]map[string]any {
		out := map[string]map[string]any{}
		for _, c := range m["changes"].([]any) {
			ch := c.(map[string]any)
			out[ch["node_id"].(string)] = ch
		}
		return out
	}
	check := func(name string, got map[string]map[string]any, want map[string]string) {
		t.Helper()
		for id, path := range want {
			c, ok := got[id]
			if !ok {
				t.Fatalf("%s: no change for %s (%s): %v", name, id, path, got)
			}
			if c["op"] != "delete" || c["deleted"] != true || c["path"] != path {
				t.Fatalf("%s: change for %s = %v, want op=delete deleted=true path=%s", name, path, c, path)
			}
			if c["size"].(float64) != 0 || c["content_hash"] != "" {
				t.Fatalf("%s: delete carries content fields: %v", name, c)
			}
		}
	}
	// Without delete-by-id (released clients delete the reported PATH) the feed serves no
	// purged rows at all, scoped or not, from any cursor.
	for _, c := range []struct {
		since  int64
		scoped bool
	}{
		{cursor, false}, {scopedCursor, true}, {0, false}, {0, true},
	} {
		url := fmt.Sprintf("/sync/changes?since=%d", c.since)
		for _, features := range []string{"", "other-feature", "delete-by-id-v2"} {
			resp := changesReqFeatures(t, h, url, tok, c.scoped, features)
			if resp["has_more"] != false {
				t.Fatalf("%s (scoped=%v, features=%q): has_more=%v, want false", url, c.scoped, features, resp["has_more"])
			}
			got := byID(resp)
			if _, ok := got[did]; ok {
				t.Fatalf("%s (scoped=%v, features=%q) serves the purged folder to an old client: %v", url, c.scoped, features, got[did])
			}
			if _, ok := got[fid]; ok {
				t.Fatalf("%s (scoped=%v, features=%q) serves the purged file to an old client: %v", url, c.scoped, features, got[fid])
			}
			if c.since == 0 {
				continue
			}
			// Every change_log row after the cursor went with the purge: nothing is left.
			if n := len(resp["changes"].([]any)); n != 0 || int64(resp["cursor"].(float64)) != c.since {
				t.Fatalf("%s (scoped=%v, features=%q): %d changes, cursor %v; want none and cursor %d", url, c.scoped, features, n, resp["cursor"], c.since)
			}
		}
	}

	whole := byID(changesReqFeatures(t, h, fmt.Sprintf("/sync/changes?since=%d", cursor), tok, false, "delete-by-id"))
	check("unscoped", whole, map[string]string{did: "sync/docs", fid: "sync/docs/f.txt"})
	if whole[did]["is_dir"] != true || whole[fid]["is_dir"] != false {
		t.Fatalf("is_dir wrong: %v %v", whole[did], whole[fid])
	}
	scoped := byID(changesReqFeatures(t, h, fmt.Sprintf("/sync/changes?since=%d", scopedCursor), tok, true, " Delete-By-ID "))
	check("scoped", scoped, map[string]string{did: "docs", fid: "docs/f.txt"})

	// A pull from 0 gets the deletes too (its client may know the ids from its uploads).
	check("full", byID(changesReqFeatures(t, h, "/sync/changes?since=0", tok, false, "foo, DELETE-BY-ID,bar")),
		map[string]string{did: "sync/docs", fid: "sync/docs/f.txt"})
	check("scoped full", byID(changesReqFeatures(t, h, "/sync/changes?since=0", tok, true, "delete-by-id")),
		map[string]string{did: "docs", fid: "docs/f.txt"})
}

// A file moved out of the sync folder, then trashed and purged: every change_log row of
// it is gone with it, the move-out included. A scoped client whose cursor predates the
// move must still learn the file left its folder, under the path it had there.
func TestScopedFeedReportsPurgedNodeMovedOut(t *testing.T) {
	ctx := context.Background()
	pool, q, svc := bootstrapPairingDB(t)
	root := t.TempDir()
	s := &Server{
		auth: svc, q: q, storageRoot: root,
		files: storage.NewFileService(pool, storage.NewLocalDisk(root)),
	}
	tok, user, err := svc.Register(ctx, "movedout@x.test", "password12")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	uid := db.UUIDString(user.ID)
	sync, err := s.files.EnsureDirByPath(ctx, uid, "sync")
	if err != nil {
		t.Fatalf("sync dir: %v", err)
	}
	if _, err := q.UpsertSyncSettings(ctx, db.UpsertSyncSettingsParams{
		UserID: user.ID, Enabled: true, FolderNodeID: sync.ID,
	}); err != nil {
		t.Fatalf("sync settings: %v", err)
	}
	outside, err := s.files.EnsureDirByPath(ctx, uid, "outside")
	if err != nil {
		t.Fatalf("outside dir: %v", err)
	}
	sid, oid := db.UUIDString(sync.ID), db.UUIDString(outside.ID)
	f, err := s.files.Push(ctx, uid, &sid, "f.txt", nil, "i", strings.NewReader("hello"))
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	fid := db.UUIDString(f.Node.ID)

	h := svc.Middleware(http.HandlerFunc(s.handleSyncChanges))
	cursor := int64(changesReq(t, h, "/sync/changes?since=0", tok, true)["cursor"].(float64))

	if _, err := s.files.Move(ctx, uid, fid, &oid); err != nil {
		t.Fatalf("move out: %v", err)
	}
	if err := s.files.Delete(ctx, uid, fid); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := s.files.Purge(ctx, uid, fid); err != nil {
		t.Fatalf("purge: %v", err)
	}
	var got map[string]any
	old := changesReq(t, h, fmt.Sprintf("/sync/changes?since=%d", cursor), tok, true)
	if n := len(old["changes"].([]any)); n != 0 || int64(old["cursor"].(float64)) != cursor || old["has_more"] != false {
		t.Fatalf("scoped pull without delete-by-id: %d changes, cursor %v, has_more %v; want none, %d, false", n, old["cursor"], old["has_more"], cursor)
	}
	for _, c := range changesReqFeatures(t, h, fmt.Sprintf("/sync/changes?since=%d", cursor), tok, true, "delete-by-id")["changes"].([]any) {
		if ch := c.(map[string]any); ch["node_id"] == fid {
			got = ch
		}
	}
	if got == nil || got["op"] != "delete" || got["deleted"] != true || got["path"] != "f.txt" {
		t.Fatalf("scoped pull since %d: change for the moved-out file = %v, want op=delete path=f.txt", cursor, got)
	}
}

// /sync/changes depends on X-Discodrive-Features: no cache may keep a response, or
// serve one to a client that sent a different header.
func TestSyncChangesNotCacheable(t *testing.T) {
	ctx := context.Background()
	pool, q, svc := bootstrapPairingDB(t)
	root := t.TempDir()
	s := &Server{
		auth: svc, q: q, storageRoot: root,
		files: storage.NewFileService(pool, storage.NewLocalDisk(root)),
	}
	tok, _, err := svc.Register(ctx, "nocache@x.test", "password12")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/sync/changes?since=0", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	svc.Middleware(http.HandlerFunc(s.handleSyncChanges)).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if got := rec.Header().Values("Vary"); !slices.Contains(got, "X-Discodrive-Features") {
		t.Fatalf("Vary = %q, want X-Discodrive-Features", got)
	}
}

// X-Discodrive-Features: comma-separated tokens, case and spaces ignored, unknown
// tokens skipped, several header lines allowed.
func TestHasFeature(t *testing.T) {
	for _, c := range []struct {
		values []string
		want   bool
	}{
		{nil, false},
		{[]string{""}, false},
		{[]string{"delete-by-id"}, true},
		{[]string{"DELETE-BY-ID"}, true},
		{[]string{"  Delete-By-Id  "}, true},
		{[]string{"foo,delete-by-id"}, true},
		{[]string{"foo , bar ,delete-by-id , baz"}, true},
		{[]string{"foo", "delete-by-id"}, true},
		{[]string{"foo,bar"}, false},
		{[]string{"delete-by-id-v2"}, false},
		{[]string{"delete by id"}, false},
		{[]string{"delete-by-id;q=1"}, false},
	} {
		r := httptest.NewRequest(http.MethodGet, "/sync/changes", nil)
		for _, v := range c.values {
			r.Header.Add("X-Discodrive-Features", v)
		}
		if got := hasFeature(r, featureDeleteByID); got != c.want {
			t.Errorf("hasFeature(%q) = %v, want %v", c.values, got, c.want)
		}
	}
}

// A purge that has to keep the folder (something under it is live again) says so:
// 409 with a message, not a silent 204.
func TestPurgeKeptFolderIs409(t *testing.T) {
	ctx := context.Background()
	pool, q, svc := bootstrapPairingDB(t)
	root := t.TempDir()
	s := &Server{
		auth: svc, q: q, storageRoot: root,
		files: storage.NewFileService(pool, storage.NewLocalDisk(root)),
	}
	tok, user, err := svc.Register(ctx, "kept@x.test", "password12")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	uid := db.UUIDString(user.ID)
	docs, err := s.files.CreateFolder(ctx, uid, nil, "docs")
	if err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	did := db.UUIDString(docs.ID)
	f, err := s.files.Push(ctx, uid, &did, "f.txt", nil, "i", strings.NewReader("x"))
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if err := s.files.Delete(ctx, uid, did); err != nil {
		t.Fatalf("delete: %v", err)
	}
	// The state a purge refuses to break: a live node under the trashed folder.
	if _, err := pool.Exec(ctx, `UPDATE nodes SET deleted_at = NULL, trash_path = NULL WHERE id = $1`, f.Node.ID); err != nil {
		t.Fatalf("revive: %v", err)
	}
	req := httptest.NewRequest(http.MethodDelete, "/files/"+did+"/purge", nil)
	req.SetPathValue("id", did)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	svc.Middleware(http.HandlerFunc(s.handlePurge)).ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "stays in the trash") {
		t.Fatalf("purge of a kept folder: %d %s, want 409 with a message", rec.Code, rec.Body.String())
	}
}

// A write that kept colliding with concurrent changes is retryable: 503 + Retry-After.
func TestBusyStorageErrorIs503(t *testing.T) {
	rec := httptest.NewRecorder()
	writeStorageErr(rec, fmt.Errorf("%w (deadlock)", storage.ErrBusy))
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("busy: %d (Retry-After %q), want 503 with Retry-After", rec.Code, rec.Header().Get("Retry-After"))
	}
}
