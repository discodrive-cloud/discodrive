package main

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func serve(h http.Handler, method, path, acceptEncoding string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if acceptEncoding != "" {
		req.Header.Set("Accept-Encoding", acceptEncoding)
	}
	rec := httptest.NewRecorder()
	compressResponses(h).ServeHTTP(rec, req)
	return rec
}

func bigJSON(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "["+strings.Repeat(`{"name":"file.txt","size":12345},`, 2000)+"{}]")
}

// A large folder listing is compressed for clients that accept gzip, and decompresses
// to exactly what the handler wrote.
func TestCompressesLargeJSON(t *testing.T) {
	rec := serve(http.HandlerFunc(bigJSON), http.MethodGet, "/files", "gzip, br")
	if rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("Content-Encoding %q, want gzip", rec.Header().Get("Content-Encoding"))
	}
	if !strings.Contains(rec.Header().Get("Vary"), "Accept-Encoding") {
		t.Fatal("missing Vary: Accept-Encoding")
	}
	compressed := rec.Body.Len()
	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(zr)
	want := httptest.NewRecorder()
	bigJSON(want, nil)
	if !bytes.Equal(got, want.Body.Bytes()) {
		t.Fatal("decompressed body differs from the handler's output")
	}
	if compressed == 0 || compressed > want.Body.Len()/4 {
		t.Fatalf("compressed %d bytes of %d", compressed, want.Body.Len())
	}
}

func TestLeavesUncompressed(t *testing.T) {
	file := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/mpeg")
		http.ServeContent(w, r, "a.mp3", time.Time{}, strings.NewReader(strings.Repeat("x", 100_000)))
	})
	sse := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, strings.Repeat("data: {}\n\n", 500))
	})
	small := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", "2")
		_, _ = io.WriteString(w, "{}")
	})
	cases := []struct {
		name, method, accept string
		h                    http.Handler
	}{
		{"client without gzip", http.MethodGet, "", http.HandlerFunc(bigJSON)},
		{"POST (may carry secrets: BREACH)", http.MethodPost, "gzip", http.HandlerFunc(bigJSON)},
		{"file download", http.MethodGet, "gzip", file},
		{"server-sent events", http.MethodGet, "gzip", sse},
		{"tiny response", http.MethodGet, "gzip", small},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := serve(c.h, c.method, "/x", c.accept)
			if ce := rec.Header().Get("Content-Encoding"); ce != "" {
				t.Fatalf("Content-Encoding %q, want none", ce)
			}
		})
	}
}

// A ranged download passes through untouched, as a 206 with its Content-Range.
func TestRangeRequestPassesThrough(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json") // compressible type, still must not be
		http.ServeContent(w, r, "a.json", time.Time{}, strings.NewReader(strings.Repeat("y", 50_000)))
	})
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("Range", "bytes=100-199")
	rec := httptest.NewRecorder()
	compressResponses(h).ServeHTTP(rec, req)
	if rec.Code != http.StatusPartialContent || rec.Header().Get("Content-Encoding") != "" || rec.Body.Len() != 100 {
		t.Fatalf("code %d, encoding %q, %d bytes", rec.Code, rec.Header().Get("Content-Encoding"), rec.Body.Len())
	}
}

// readFromRecorder records whether a body arrived through io.ReaderFrom — the path
// net/http turns into sendfile for files.
type readFromRecorder struct {
	*httptest.ResponseRecorder
	usedReadFrom bool
}

func (r *readFromRecorder) ReadFrom(src io.Reader) (int64, error) {
	r.usedReadFrom = true
	return io.Copy(r.ResponseRecorder, src)
}

// File downloads that are not compressed keep the zero-copy path: the wrapper must hand
// ReadFrom to the real writer instead of pushing the bytes through itself.
func TestUncompressedKeepsReaderFrom(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Content-Length", "100000")
		w.WriteHeader(http.StatusOK)
		// LimitReader, like http.ServeContent's io.CopyN: no WriteTo, so io.Copy
		// takes the destination's ReadFrom.
		_, _ = io.Copy(w, io.LimitReader(strings.NewReader(strings.Repeat("v", 100_000)), 100_000))
	})
	rec := &readFromRecorder{ResponseRecorder: httptest.NewRecorder()}
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	compressResponses(h).ServeHTTP(rec, req)
	if !rec.usedReadFrom {
		t.Fatal("an uncompressed download no longer reaches the writer's ReadFrom (sendfile lost)")
	}
	if rec.Body.Len() != 100_000 {
		t.Fatalf("%d bytes", rec.Body.Len())
	}
}

// WebDAV folder listings are 207 Multi-Status XML — the largest responses WebDAV sends.
func TestCompressesPropfindMultiStatus(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/xml; charset=utf-8")
		w.WriteHeader(http.StatusMultiStatus)
		_, _ = io.WriteString(w, "<multistatus>"+strings.Repeat("<response><href>/dav/a.txt</href></response>", 500)+"</multistatus>")
	})
	rec := serve(h, "PROPFIND", "/dav/", "gzip")
	if rec.Code != http.StatusMultiStatus || rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("code %d, encoding %q, want 207 gzip", rec.Code, rec.Header().Get("Content-Encoding"))
	}
}
