package api

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

// A multipart upload goes straight into storage staging, where the quota and the upload
// slots apply. It used to be spooled to the temp directory first — for an account without
// a quota that was up to 10 GiB per request on the system disk. With an unusable TMPDIR
// any spooling fails, so the upload succeeding shows there is none.
func TestMultipartUploadIsNotSpooledToTmp(t *testing.T) {
	_, do := modTimeServer(t)
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("name", "big.bin")
	fw, _ := mw.CreateFormFile("file", "big.bin")
	_, _ = io.CopyN(fw, zeros{}, 4<<20) // far above what a form parser keeps in memory
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/files/upload", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := do(req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("code = %d (%s), want 201", rec.Code, rec.Body)
	}
	var out struct {
		Node struct {
			Name string `json:"name"`
			Size int64  `json:"size"`
		} `json:"node"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Node.Name != "big.bin" || out.Node.Size != 4<<20 {
		t.Errorf("stored %q of %d bytes, want big.bin of %d", out.Node.Name, out.Node.Size, 4<<20)
	}
}

// The file is stored while it is being read, so everything that says where and how to
// store it has to come first. A field after the file is refused, and nothing is stored:
// silently ignoring a late parent_id would put the file into the wrong folder.
func TestMultipartFieldAfterTheFileIsRefused(t *testing.T) {
	_, do := modTimeServer(t)

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "late.txt")
	_, _ = fw.Write([]byte("content"))
	_ = mw.WriteField("name", "renamed.txt")
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/files/upload", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if rec := do(req); rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d (%s), want 400", rec.Code, rec.Body)
	}
	list := do(httptest.NewRequest(http.MethodGet, "/files", nil))
	var nodes []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &nodes); err != nil {
		t.Fatalf("list: %v (%s)", err, list.Body)
	}
	if len(nodes) != 0 {
		t.Errorf("the refused upload left %v in the tree", nodes)
	}
}
