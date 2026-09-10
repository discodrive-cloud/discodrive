package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"discodrive/internal/storage"
)

// The chunked protocol as a sync transport: a session opened by path lands where a
// PUT /sync/file would, honours the daemon's scope, and carries a base version so a
// file changed on the server meanwhile becomes a conflict copy, not a silent overwrite.

func (s *Server) chunkRoutesForTest() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /sync/file", s.handleSyncPutFile)
	mux.HandleFunc("POST /upload/init", s.handleUploadInit)
	mux.HandleFunc("PUT /upload/{id}/chunk/{n}", s.handleUploadChunk)
	mux.HandleFunc("POST /upload/{id}/complete", s.handleUploadComplete)
	mux.HandleFunc("GET /files", s.handleListFiles)
	mux.HandleFunc("PUT /me/sync", s.handlePutSyncSettings)
	mux.HandleFunc("POST /sync/dir", s.handleSyncMkdir)
	return mux
}

// chunkServer is modTimeServer with the routes these tests exercise.
func chunkServer(t *testing.T) chunkClient {
	t.Helper()
	pool, q, svc := bootstrapPairingDB(t)
	root := t.TempDir()
	s := &Server{auth: svc, q: q, files: storage.NewFileService(pool, storage.NewLocalDisk(root)), storageRoot: root}
	s.uploads = storage.NewUploads(storage.NewLocalDisk(root), s.files)
	tok, _, err := svc.Register(context.Background(), "chunks@x.test", "password12")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	return chunkClient{t, func(req *http.Request) *httptest.ResponseRecorder {
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		svc.Middleware(s.chunkRoutesForTest()).ServeHTTP(rec, req)
		return rec
	}}
}

type chunkClient struct {
	t  *testing.T
	do func(*http.Request) *httptest.ResponseRecorder
}

func (c chunkClient) json(method, path string, body any, headers ...string) (*httptest.ResponseRecorder, map[string]any) {
	c.t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := c.do(req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

// sendWhole opens a session with the given init body, sends the content as one chunk and
// completes it; returns the complete response.
func (c chunkClient) sendWhole(init map[string]any, content string, headers ...string) (*httptest.ResponseRecorder, map[string]any) {
	c.t.Helper()
	init["size"] = len(content)
	rec, out := c.json("POST", "/upload/init", init, headers...)
	if rec.Code != http.StatusCreated {
		c.t.Fatalf("init: %d %s", rec.Code, rec.Body.String())
	}
	id := out["upload_id"].(string)
	req := httptest.NewRequest("PUT", "/upload/"+id+"/chunk/0", strings.NewReader(content))
	req.Header.Set("Content-Type", "application/octet-stream")
	if rec := c.do(req); rec.Code != http.StatusOK {
		c.t.Fatalf("chunk: %d %s", rec.Code, rec.Body.String())
	}
	return c.json("POST", "/upload/"+id+"/complete", nil)
}

func listNames(t *testing.T, c chunkClient, parent string) []string {
	t.Helper()
	path := "/files"
	if parent != "" {
		path += "?parent_id=" + parent
	}
	rec := c.do(httptest.NewRequest("GET", path, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	var nodes []nodeDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &nodes); err != nil {
		t.Fatalf("decode listing: %v (%s)", err, rec.Body.String())
	}
	var names []string
	for _, n := range nodes {
		names = append(names, n.Name)
	}
	return names
}

func TestUploadInitByPathLandsLikeASyncPut(t *testing.T) {
	c := chunkServer(t)
	rec, out := c.sendWhole(map[string]any{"path": "docs/notes/a.txt"}, "hello")
	if rec.Code != http.StatusCreated {
		t.Fatalf("complete: %d %s", rec.Code, rec.Body.String())
	}
	if out["conflicted"] != false {
		t.Fatalf("a fresh file must not be a conflict: %v", out)
	}
	node := out["node"].(map[string]any)
	if node["name"] != "a.txt" {
		t.Fatalf("node = %v", node)
	}
	// The folder chain was created on the way, as PUT /sync/file does.
	if names := listNames(t, c, ""); len(names) != 1 || names[0] != "docs" {
		t.Fatalf("root = %v, want [docs]", names)
	}
}

func TestUploadInitRefusesPathTogetherWithParent(t *testing.T) {
	c := chunkServer(t)
	rec, _ := c.json("POST", "/upload/init", map[string]any{"path": "a.txt", "name": "a.txt", "size": 1})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400 for path together with name, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestUploadBaseVersionMismatchBecomesAConflictCopy(t *testing.T) {
	c := chunkServer(t)
	// Version 1 lands; someone else makes version 2 through a sync PUT.
	_, out := c.sendWhole(map[string]any{"path": "a.txt"}, "v1")
	v1 := int64(out["node"].(map[string]any)["version"].(float64))
	req := httptest.NewRequest("PUT", "/sync/file?path=a.txt", strings.NewReader("v2"))
	if rec := c.do(req); rec.Code != http.StatusCreated {
		t.Fatalf("sync put: %d %s", rec.Code, rec.Body.String())
	}
	// A chunked upload still based on v1 must not overwrite v2.
	rec, out := c.sendWhole(map[string]any{"path": "a.txt", "base_version": v1}, "v1-edited")
	if rec.Code != http.StatusCreated {
		t.Fatalf("complete: %d %s", rec.Code, rec.Body.String())
	}
	if out["conflicted"] != true {
		t.Fatalf("stale base version must be reported as a conflict: %v", out)
	}
	names := listNames(t, c, "")
	conflict := false
	for _, n := range names {
		if strings.Contains(n, "(conflict") {
			conflict = true
		}
	}
	if !conflict || len(names) != 2 {
		t.Fatalf("want a.txt plus a conflict copy, got %v", names)
	}
}

func TestUploadBaseVersionMatchIsAccepted(t *testing.T) {
	c := chunkServer(t)
	_, out := c.sendWhole(map[string]any{"path": "b.txt"}, "v1")
	v1 := int64(out["node"].(map[string]any)["version"].(float64))
	rec, out := c.sendWhole(map[string]any{"path": "b.txt", "base_version": v1}, "v2")
	if rec.Code != http.StatusCreated || out["conflicted"] != false {
		t.Fatalf("matching base must be accepted: %d %v", rec.Code, out)
	}
	if v := int64(out["node"].(map[string]any)["version"].(float64)); v != v1+1 {
		t.Fatalf("version = %d, want %d", v, v1+1)
	}
	if names := listNames(t, c, ""); len(names) != 1 {
		t.Fatalf("want one file, got %v", names)
	}
}

func TestUploadInitByPathHonoursTheDaemonScope(t *testing.T) {
	c := chunkServer(t)
	// A folder to scope to, then the scope itself (what the desktop app's settings do).
	rec, out := c.json("POST", "/sync/dir", map[string]any{"path": "Scoped"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("mkdir: %d %s", rec.Code, rec.Body.String())
	}
	scopedID := out["node"].(map[string]any)["id"].(string)
	if rec, _ := c.json("PUT", "/me/sync", map[string]any{"enabled": true, "folderNodeId": scopedID}); rec.Code >= 300 {
		t.Fatalf("set scope: %d %s", rec.Code, rec.Body.String())
	}
	// With the scope header the path is relative to the scoped folder, as for PUT /sync/file.
	rec, _ = c.sendWhole(map[string]any{"path": "inside.txt"}, "x", "X-Discodrive-Scope", "1")
	if rec.Code != http.StatusCreated {
		t.Fatalf("complete: %d %s", rec.Code, rec.Body.String())
	}
	if names := listNames(t, c, scopedID); len(names) != 1 || names[0] != "inside.txt" {
		t.Fatalf("scoped folder = %v, want [inside.txt]", names)
	}
}
