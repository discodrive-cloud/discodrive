package podcast

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// loopback returns the production download client with only its dialer swapped, so the
// test server (on 127.0.0.1, which the SSRF guard blocks) is reachable.
func loopback(c *http.Client) *http.Client {
	cp := *c
	cp.Transport = &http.Transport{}
	return &cp
}

// An episode takes minutes to arrive; the 30-second budget of a feed fetch must not apply
// to it. Here the budget is 150 ms and the body takes about 600 ms.
func TestDownloadClient_OutlivesTheFeedTimeout(t *testing.T) {
	old := httpTimeout
	httpTimeout = 150 * time.Millisecond
	defer func() { httpTimeout = old }()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/mpeg")
		for i := 0; i < 6; i++ {
			_, _ = w.Write([]byte(strings.Repeat("a", 1000)))
			w.(http.Flusher).Flush()
			time.Sleep(100 * time.Millisecond)
		}
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "ep.mp3")
	size, _, _, err := DownloadToUnsafe(context.Background(), loopback(newDownloadClient()), srv.URL+"/ep.mp3", dest, 0)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if size != 6000 {
		t.Errorf("size = %d, want 6000", size)
	}
}

// With no whole-exchange deadline a host that stops sending must still be given up on.
func TestDownload_AbortsAStalledBody(t *testing.T) {
	old := stallTimeout
	stallTimeout = 150 * time.Millisecond
	defer func() { stallTimeout = old }()

	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100000")
		_, _ = w.Write([]byte("partial"))
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)

	dest := filepath.Join(t.TempDir(), "ep.mp3")
	done := make(chan error, 1)
	go func() {
		_, _, _, err := DownloadToUnsafe(context.Background(), loopback(newDownloadClient()), srv.URL+"/ep.mp3", dest, 0)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a stalled download reported success")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a stalled download never returned")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Errorf("partial file left behind (stat err = %v)", err)
	}
}
