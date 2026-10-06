package api

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"discodrive/internal/storage"
)

// modTimeServer wires a Server with a real FileService over a throwaway Postgres, plus an
// authenticated request helper — the transports are what these tests are about.
func modTimeServer(t *testing.T) (*Server, func(*http.Request) *httptest.ResponseRecorder) {
	t.Helper()
	pool, q, svc := bootstrapPairingDB(t)
	root := t.TempDir()
	s := &Server{
		auth:        svc,
		q:           q,
		files:       storage.NewFileService(pool, storage.NewLocalDisk(root)),
		storageRoot: root,
	}
	s.uploads = storage.NewUploads(storage.NewLocalDisk(root), s.files)

	tok, _, err := svc.Register(context.Background(), "mtime@x.test", "password12")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	do := func(req *http.Request) *httptest.ResponseRecorder {
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		svc.Middleware(s.routesForTest()).ServeHTTP(rec, req)
		return rec
	}
	return s, do
}

// routesForTest exposes just the handlers these tests exercise.
func (s *Server) routesForTest() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /files/upload", s.handleUpload)
	mux.HandleFunc("PUT /sync/file", s.handleSyncPutFile)
	mux.HandleFunc("POST /upload/init", s.handleUploadInit)
	mux.HandleFunc("GET /files", s.handleListFiles)
	mux.HandleFunc("POST /files/folder", s.handleCreateFolder)
	mux.HandleFunc("PATCH /files/{id}/relocate", s.handleRelocate)
	return mux
}

func uploadForm(t *testing.T, name, content, modifiedAt string) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("name", name)
	if modifiedAt != "" {
		_ = mw.WriteField("modified_at", modifiedAt)
	}
	fw, err := mw.CreateFormFile("file", name)
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := fw.Write([]byte(content)); err != nil {
		t.Fatalf("write part: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart: %v", err)
	}
	return &buf, mw.FormDataContentType()
}

// TestUploadCarriesClientModifiedAt: the date survives POST /files/upload and comes back
// out of the listing, together with content_hash.
func TestUploadCarriesClientModifiedAt(t *testing.T) {
	_, do := modTimeServer(t)
	want := "2019-06-15T12:30:00Z"

	body, ctype := uploadForm(t, "photo.jpg", "jpegbytes", want)
	req := httptest.NewRequest(http.MethodPost, "/files/upload", body)
	req.Header.Set("Content-Type", ctype)
	if rec := do(req); rec.Code != http.StatusCreated {
		t.Fatalf("upload: code=%d body=%s", rec.Code, rec.Body.String())
	}

	rec := do(httptest.NewRequest(http.MethodGet, "/files", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("list: code=%d body=%s", rec.Code, rec.Body.String())
	}
	var nodes []nodeDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &nodes); err != nil {
		t.Fatalf("decode listing: %v (%s)", err, rec.Body.String())
	}
	if len(nodes) != 1 {
		t.Fatalf("listing has %d nodes, want 1", len(nodes))
	}
	if got := nodes[0].ModifiedAt.UTC().Format(time.RFC3339); got != want {
		t.Fatalf("modified_at = %s, want %s", got, want)
	}
	if nodes[0].ContentHash == "" {
		t.Fatal("content_hash is absent from the listing, so a client still cannot dedup from it")
	}
}

// TestSyncPutCarriesClientModifiedAt covers the daemon's route: the X-Modified-At header.
func TestSyncPutCarriesClientModifiedAt(t *testing.T) {
	_, do := modTimeServer(t)
	want := "2020-01-02T03:04:05Z"

	req := httptest.NewRequest(http.MethodPut, "/sync/file?path=note.txt", strings.NewReader("hi"))
	req.Header.Set("X-Modified-At", want)
	rec := do(req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("sync put: code=%d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Node nodeDTO `json:"node"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := out.Node.ModifiedAt.UTC().Format(time.RFC3339); got != want {
		t.Fatalf("modified_at = %s, want %s", got, want)
	}
}

// TestUploadRejectsMalformedModifiedAt: a bad date is an error, not a silent fallback to
// "now" — a client that thinks it preserved the date deserves to be told it did not.
func TestUploadRejectsMalformedModifiedAt(t *testing.T) {
	_, do := modTimeServer(t)

	body, ctype := uploadForm(t, "x.txt", "data", "15 June 2019")
	req := httptest.NewRequest(http.MethodPost, "/files/upload", body)
	req.Header.Set("Content-Type", ctype)
	if rec := do(req); rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed modified_at: code=%d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/upload/init",
		strings.NewReader(`{"name":"y.txt","size":4,"modified_at":"nonsense"}`))
	req.Header.Set("Content-Type", "application/json")
	if rec := do(req); rec.Code != http.StatusBadRequest {
		t.Fatalf("upload/init malformed modified_at: code=%d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
}

// TestUploadWithoutModifiedAtStaysServerDated is the compatibility guarantee for every
// client that has not been taught the new field.
func TestUploadWithoutModifiedAtStaysServerDated(t *testing.T) {
	_, do := modTimeServer(t)
	before := time.Now().Add(-time.Minute)

	body, ctype := uploadForm(t, "plain.txt", "data", "")
	req := httptest.NewRequest(http.MethodPost, "/files/upload", body)
	req.Header.Set("Content-Type", ctype)
	rec := do(req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload: code=%d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Node nodeDTO `json:"node"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Node.ModifiedAt.Before(before) {
		t.Fatalf("modified_at = %s, want roughly now", out.Node.ModifiedAt)
	}
}
