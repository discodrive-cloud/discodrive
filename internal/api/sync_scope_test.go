package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/db"
	"discodrive/internal/storage"
)

// createNodeAt inserts a node with an explicit disk_path (needed to exercise scope filtering).
func createNodeAt(t *testing.T, ctx context.Context, q *db.Queries, uid pgtype.UUID, name, diskPath string, isDir bool) db.Node {
	t.Helper()
	n, err := q.CreateNode(ctx, db.CreateNodeParams{
		UserID:   uid,
		Name:     name,
		IsDir:    isDir,
		DiskPath: pgtype.Text{String: diskPath, Valid: true},
	})
	if err != nil {
		t.Fatalf("createNodeAt %s: %v", name, err)
	}
	return n
}

// changesReq issues GET /sync/changes; when scoped, it sets the daemon's X-Discodrive-Scope header.
func changesReq(t *testing.T, handler http.Handler, url, bearer string, scoped bool) map[string]any {
	t.Helper()
	return changesReqFeatures(t, handler, url, bearer, scoped, "")
}

// changesReqFeatures is changesReq with an X-Discodrive-Features header (none when "").
func changesReqFeatures(t *testing.T, handler http.Handler, url, bearer string, scoped bool, features string) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Authorization", "Bearer "+bearer)
	if scoped {
		req.Header.Set("X-Discodrive-Scope", "1")
	}
	if features != "" {
		req.Header.Set("X-Discodrive-Features", features)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s (scoped=%v): code=%d body=%s", url, scoped, rec.Code, rec.Body.String())
	}
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	return m
}

// changePaths extracts the "path" of each change in a /sync/changes response.
func changePaths(m map[string]any) []string {
	var out []string
	for _, c := range m["changes"].([]any) {
		out = append(out, c.(map[string]any)["path"].(string))
	}
	return out
}

func TestSyncScope(t *testing.T) {
	ctx := context.Background()
	pool, q, svc := bootstrapPairingDB(t)
	root := t.TempDir()
	s := &Server{
		auth:        svc,
		q:           q,
		files:       storage.NewFileService(pool, storage.NewLocalDisk(root)),
		storageRoot: root,
	}

	// --- Task 3: /me/sync GET default + PUT round-trip (epoch bump) ---
	t.Run("settings round-trip", func(t *testing.T) {
		tok, user, err := svc.Register(ctx, "scope1@x.test", "password12")
		if err != nil {
			t.Fatalf("register: %v", err)
		}
		dir := createNodeAt(t, ctx, q, user.ID, "sync", db.UUIDString(user.ID)+"/sync", true)

		getH := svc.Middleware(http.HandlerFunc(s.handleGetSyncSettings))
		putH := svc.Middleware(http.HandlerFunc(s.handlePutSyncSettings))

		_, m := doGet(getH, "/me/sync", tok)
		if m["enabled"] != false || m["folder"] != nil {
			t.Fatalf("default GET: %+v", m)
		}

		_, m = doPut(putH, "/me/sync", tok, map[string]any{"enabled": true, "folderNodeId": db.UUIDString(dir.ID)})
		if m["enabled"] != true {
			t.Fatalf("PUT enabled: %+v", m)
		}
		folder, _ := m["folder"].(map[string]any)
		if folder == nil || folder["name"] != "sync" {
			t.Fatalf("PUT folder: %+v", m)
		}
		if m["epoch"].(float64) != 1 {
			t.Fatalf("expected epoch 1, got %v", m["epoch"])
		}
	})

	// --- Task 4: /sync/changes scoped ONLY when the daemon header is present ---
	t.Run("changes scoped only with header", func(t *testing.T) {
		tok, user, err := svc.Register(ctx, "scope2@x.test", "password12")
		if err != nil {
			t.Fatalf("register: %v", err)
		}
		uidStr := db.UUIDString(user.ID)
		dir := createNodeAt(t, ctx, q, user.ID, "sync", uidStr+"/sync", true)
		inside := createNodeAt(t, ctx, q, user.ID, "a.txt", uidStr+"/sync/a.txt", false)
		outside := createNodeAt(t, ctx, q, user.ID, "b.txt", uidStr+"/b.txt", false)
		mustAppend(t, ctx, q, user.ID, inside.ID, 1)
		mustAppend(t, ctx, q, user.ID, outside.ID, 2)

		if _, err := q.UpsertSyncSettings(ctx, db.UpsertSyncSettingsParams{
			UserID: user.ID, Enabled: true, FolderNodeID: dir.ID,
		}); err != nil {
			t.Fatalf("UpsertSyncSettings: %v", err)
		}

		h := svc.Middleware(http.HandlerFunc(s.handleSyncChanges))

		// WITHOUT header → whole vault, unaffected (both files, full paths).
		whole := changePaths(changesReq(t, h, "/sync/changes?since=0", tok, false))
		if !contains(whole, "sync/a.txt") || !contains(whole, "b.txt") {
			t.Fatalf("unscoped feed must show whole vault, got %v", whole)
		}

		// WITH header → only files under "sync", rebased relative to it, plus scope_epoch.
		scopedResp := changesReq(t, h, "/sync/changes?since=0", tok, true)
		scoped := changePaths(scopedResp)
		if len(scoped) != 1 || scoped[0] != "a.txt" {
			t.Fatalf("scoped feed must be [a.txt] (rebased), got %v", scoped)
		}
		if scopedResp["scope_epoch"].(float64) != 1 {
			t.Fatalf("expected scope_epoch 1, got %v", scopedResp["scope_epoch"])
		}
	})

	// --- Task 5: push prefixes the path ONLY when the daemon header is present ---
	t.Run("push prefixed only with header", func(t *testing.T) {
		tok, user, err := svc.Register(ctx, "scope3@x.test", "password12")
		if err != nil {
			t.Fatalf("register: %v", err)
		}
		// Create the scope folder through the storage service (proper node + disk_path).
		dir, err := s.files.EnsureDirByPath(ctx, db.UUIDString(user.ID), "sync")
		if err != nil {
			t.Fatalf("EnsureDirByPath: %v", err)
		}
		if _, err := q.UpsertSyncSettings(ctx, db.UpsertSyncSettingsParams{
			UserID: user.ID, Enabled: true, FolderNodeID: dir.ID,
		}); err != nil {
			t.Fatalf("UpsertSyncSettings: %v", err)
		}

		putH := svc.Middleware(http.HandlerFunc(s.handleSyncPutFile))

		// Daemon push (header) of "a.txt" must land under the scope folder.
		putFile(t, putH, "/sync/file?path=a.txt", tok, "hi-daemon", true)
		// Non-daemon push (no header) of "root.txt" must land at the vault root.
		putFile(t, putH, "/sync/file?path=root.txt", tok, "hi-app", false)

		// Inspect via the unscoped (whole-vault) feed.
		h := svc.Middleware(http.HandlerFunc(s.handleSyncChanges))
		paths := changePaths(changesReq(t, h, "/sync/changes?since=0", tok, false))
		if !contains(paths, "sync/a.txt") {
			t.Fatalf("daemon push must be prefixed to sync/a.txt, got %v", paths)
		}
		if !contains(paths, "root.txt") || contains(paths, "sync/root.txt") {
			t.Fatalf("app push must NOT be prefixed, got %v", paths)
		}
	})
}

func mustAppend(t *testing.T, ctx context.Context, q *db.Queries, uid, nodeID pgtype.UUID, seq int64) {
	t.Helper()
	if _, err := q.AppendChange(ctx, db.AppendChangeParams{UserID: uid, NodeID: nodeID, Seq: seq, Op: "create", Version: 1}); err != nil {
		t.Fatalf("AppendChange seq %d: %v", seq, err)
	}
}

func putFile(t *testing.T, handler http.Handler, url, bearer, body string, scoped bool) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, url, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+bearer)
	if scoped {
		req.Header.Set("X-Discodrive-Scope", "1")
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("PUT %s (scoped=%v): code=%d body=%s", url, scoped, rec.Code, rec.Body.String())
	}
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
