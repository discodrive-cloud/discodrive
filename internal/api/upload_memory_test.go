package api

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
)

// A multipart upload used to be parsed into memory up to 32 MiB per request (the buffer
// grows by doubling, so ~2x that), before any limit on concurrent uploads applied:
// eight parallel 30 MB uploads OOM-killed a 1 GB machine. Large parts must spill to disk.
func TestMultipartUploadDoesNotBufferFileInMemory(t *testing.T) {
	_, do := modTimeServer(t)
	const size = 16 << 20

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("file", "big.bin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.CopyN(fw, zeros{}, size); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/files/upload", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	rec := do(req)
	runtime.ReadMemStats(&after)

	if rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > size/2 {
		t.Fatalf("handling a %d MiB upload allocated %d MiB: the file was buffered in memory", size>>20, alloc>>20)
	}
}

type zeros struct{}

func (zeros) Read(p []byte) (int, error) { clear(p); return len(p), nil }
