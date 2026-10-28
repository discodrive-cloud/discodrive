package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Windows' WebDAV client strips the trailing slash and asks for /dav itself. A redirect
// there makes Explorer give up with "the network name cannot be found", so the share root
// must reach the DAV handler with or without the slash.
func TestDAVRootWithoutSlash(t *testing.T) {
	var got string
	dav := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Path
		w.WriteHeader(http.StatusMultiStatus)
	})
	h := NewRouter(nil, nil, nil, nil, "", nil, nil, nil, dav, nil, nil, nil, false, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	for _, path := range []string{"/dav", "/dav/"} {
		got = ""
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("PROPFIND", path, nil))
		if w.Code != http.StatusMultiStatus || got != path {
			t.Fatalf("PROPFIND %s: status %d, handler saw %q (Location %q)", path, w.Code, got, w.Header().Get("Location"))
		}
	}
}
