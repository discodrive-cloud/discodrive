package httpsecurity

import (
	"bytes"
	"io"
	"net/http/httptest"
	"testing"
)

type brokenBody struct{}

func (brokenBody) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (brokenBody) Close() error             { return nil }

func TestReadDAVBodyBoundaryAndErrors(t *testing.T) {
	for _, n := range []int{0, 16, 17} {
		for _, unknown := range []bool{false, true} {
			r := httptest.NewRequest("PUT", "/", bytes.NewReader(bytes.Repeat([]byte("x"), n)))
			if unknown {
				r.ContentLength = -1
			}
			w := httptest.NewRecorder()
			body, ok := ReadDAVBody(w, r, 16)
			if n <= 16 {
				if !ok || len(body) != n {
					t.Fatalf("n=%d ok=%v read=%d", n, ok, len(body))
				}
			} else if ok || w.Code != 413 {
				t.Fatalf("oversized: %v %d", ok, w.Code)
			}
		}
	}
	r := httptest.NewRequest("PROPPATCH", "/", nil)
	r.Body = brokenBody{}
	r.ContentLength = -1
	w := httptest.NewRecorder()
	if _, ok := ReadDAVBody(w, r, 16); ok || w.Code != 400 {
		t.Fatalf("partial object accepted: %v %d", ok, w.Code)
	}
}
