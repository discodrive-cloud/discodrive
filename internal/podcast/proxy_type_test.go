package podcast

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Hosts that label audio as octet-stream are played by the URL's extension;
// neither a generic type on a non-media URL nor an active type is let through.
func TestProxyStream_GenericTypeUsesURLExtension(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", r.URL.Query().Get("ct"))
		_, _ = w.Write([]byte("ID3AUDIO"))
	}))
	defer srv.Close()

	cases := []struct {
		path, upstream, want string // want "" = refused
	}{
		{"/ep.mp3", "application/octet-stream", "audio/mpeg"},
		{"/ep.M4B", "binary/octet-stream", "audio/mp4"},
		{"/ep.mp3", "", "audio/mpeg"},
		{"/ep.mp3", "audio/mp3", "audio/mpeg"},
		{"/ep.m4a", "audio/x-m4b", "audio/mp4"},
		{"/ep.html", "application/octet-stream", ""},
		{"/ep", "application/octet-stream", ""},
		{"/ep.mp3", "text/html", ""},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rec := httptest.NewRecorder()
		u := srv.URL + tc.path + "?ct=" + tc.upstream
		committed, err := ProxyStreamUnsafe(context.Background(), http.DefaultClient, rec, req, u)
		if tc.want == "" {
			if err == nil || committed {
				t.Errorf("%s %q: served as %q, want refused", tc.path, tc.upstream, rec.Header().Get("Content-Type"))
			}
			continue
		}
		if err != nil {
			t.Errorf("%s %q: %v, want %s", tc.path, tc.upstream, err, tc.want)
			continue
		}
		if got := rec.Header().Get("Content-Type"); got != tc.want {
			t.Errorf("%s %q: Content-Type %q, want %q", tc.path, tc.upstream, got, tc.want)
		}
		if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: nosniff missing", tc.path)
		}
	}
}
