package podcast

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProxyRejectsActiveContent(t *testing.T) {
	for _, ct := range []string{"text/html", "application/javascript", "image/svg+xml", "application/octet-stream"} {
		t.Run(ct, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", ct)
				w.Write([]byte("active content"))
			}))
			defer upstream.Close()
			rec := httptest.NewRecorder()
			committed, err := ProxyStreamUnsafe(context.Background(), upstream.Client(), rec, httptest.NewRequest("GET", "/", nil), upstream.URL)
			if err == nil || committed || rec.Body.Len() != 0 {
				t.Fatalf("active response leaked: committed=%v err=%v body=%q", committed, err, rec.Body.String())
			}
		})
	}
}
