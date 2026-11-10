package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"discodrive/internal/db"
	"discodrive/internal/storage"
)

// A scoped delete of "/" resolved to the sync folder itself: one malformed request from a
// client put the user's whole synced tree in the trash.
func TestScopedSyncDelete_RootPathDoesNotDeleteTheSyncFolder(t *testing.T) {
	ctx := context.Background()
	pool, q, svc := bootstrapPairingDB(t)
	root := t.TempDir()
	s := &Server{
		auth:        svc,
		q:           q,
		files:       storage.NewFileService(pool, storage.NewLocalDisk(root)),
		storageRoot: root,
	}
	tok, user, err := svc.Register(ctx, "delroot@x.test", "password12")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	uid := db.UUIDString(user.ID)
	dir, err := s.files.EnsureDirByPath(ctx, uid, "sync")
	if err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if _, err := s.files.PushByPath(ctx, uid, "sync/note.md", nil, strings.NewReader("keep me")); err != nil {
		t.Fatalf("push: %v", err)
	}
	putH := svc.Middleware(http.HandlerFunc(s.handlePutSyncSettings))
	if _, m := doPut(putH, "/me/sync", tok, map[string]any{"enabled": true, "folderNodeId": db.UUIDString(dir.ID)}); m["enabled"] != true {
		t.Fatalf("enable sync: %+v", m)
	}

	delH := svc.Middleware(http.HandlerFunc(s.handleSyncDelete))
	for _, p := range []string{"/", "//", ".", "./"} {
		req := httptest.NewRequest(http.MethodDelete, "/sync/file?path="+p, nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("X-Discodrive-Scope", "1")
		rec := httptest.NewRecorder()
		delH.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("DELETE path=%q: code=%d, want 400", p, rec.Code)
		}
	}
	if _, err := q.GetLiveNodeByPath(ctx, db.GetLiveNodeByPathParams{UserID: user.ID, Path: uid + "/sync"}); err != nil {
		t.Fatalf("the sync folder is gone: %v", err)
	}
	if _, err := q.GetLiveNodeByPath(ctx, db.GetLiveNodeByPathParams{UserID: user.ID, Path: uid + "/sync/note.md"}); err != nil {
		t.Fatalf("the file inside the sync folder is gone: %v", err)
	}
}
