package music

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCoverUnchanged(t *testing.T) {
	first := httptest.NewRecorder()
	if CoverUnchanged(first, httptest.NewRequest(http.MethodGet, "/c", nil), "abc123") {
		t.Fatal("a first request was answered as unchanged")
	}
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag on the cover response")
	}

	again := httptest.NewRequest(http.MethodGet, "/c", nil)
	again.Header.Set("If-None-Match", `"other", W/`+etag)
	rec := httptest.NewRecorder()
	if !CoverUnchanged(rec, again, "abc123") || rec.Code != http.StatusNotModified {
		t.Fatalf("a client holding the cover got %d, want 304", rec.Code)
	}

	// The audio file was replaced (new tags, new picture): the old cover no longer matches.
	rec = httptest.NewRecorder()
	if CoverUnchanged(rec, again, "def456") {
		t.Fatal("the cover of a changed file was answered as unchanged")
	}
	// Without a content hash there is nothing to validate against.
	if CoverUnchanged(httptest.NewRecorder(), again, "") {
		t.Fatal("unchanged without a hash")
	}
}
