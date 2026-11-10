package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// Files under _nuxt/ carry a content hash in their name: a browser may keep them for
// good. Served without any caching header (and embedded files have no modification time
// to revalidate by), every page load fetched the whole bundle again. The HTML that names
// them must be checked each time, or a new build would not be picked up.
func TestSPA_HashedAssetsAreCachedAndHTMLIsNot(t *testing.T) {
	h := spaHandler(fstest.MapFS{
		"index.html":       {Data: []byte("<html></html>")},
		"files/index.html": {Data: []byte("<html></html>")},
		"_nuxt/app.abc.js": {Data: []byte("x")},
		"favicon.ico":      {Data: []byte("x")},
	})
	get := func(p string) http.Header {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d", p, rec.Code)
		}
		return rec.Header()
	}
	if cc := get("/_nuxt/app.abc.js").Get("Cache-Control"); !strings.Contains(cc, "immutable") || !strings.Contains(cc, "max-age=31536000") {
		t.Errorf("_nuxt asset Cache-Control = %q, want a year, immutable", cc)
	}
	for _, p := range []string{"/", "/files", "/some/client/route", "/favicon.ico"} {
		if cc := get(p).Get("Cache-Control"); cc != "no-cache" {
			t.Errorf("%s Cache-Control = %q, want no-cache", p, cc)
		}
	}
}
