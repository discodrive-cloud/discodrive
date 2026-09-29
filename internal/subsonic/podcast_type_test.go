package subsonic

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"discodrive/internal/db"
)

// Downloaded episodes keep the type the host sent. audio/mp3, audio/x-m4b and
// octet-stream are common and must still play (as the allowlisted type the
// synonym or the file extension implies), not answer 403.
func TestStreamDownloadedEpisode_NonStandardContentType(t *testing.T) {
	restore := overrideFetchFeed()
	defer restore()

	h, ctx, pool := setupWithPool(t)
	h.storageRoot = t.TempDir()
	h.xaccel = false

	srv := startRSSServer(t)
	if resp := doGet(h, testAPIKey, "createPodcastChannel", "url="+srv.URL+"/feed.xml"); resp["status"] != "ok" {
		t.Fatalf("createPodcastChannel: %v", resp)
	}
	epID := getEpisodeID(t, h)
	user, err := h.q.GetUserByEmail(ctx, testEmail)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct{ stored, file, want string }{
		{"audio/mp3", "e.mp3", "audio/mpeg"},
		{"audio/x-mpeg", "e.mp3", "audio/mpeg"},
		{"audio/x-m4b", "e.m4b", "audio/mp4"},
		{"application/octet-stream", "e.mp3", "audio/mpeg"},
		{"application/octet-stream", "e.m4a", "audio/mp4"},
	}
	for _, tc := range cases {
		rel := filepath.Join("podcasts", db.UUIDString(user.ID), tc.file)
		abs := filepath.Join(h.storageRoot, rel)
		_ = os.MkdirAll(filepath.Dir(abs), 0o755)
		if err := os.WriteFile(abs, fakeEpisodeAudio, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `UPDATE podcast_episodes SET disk_path = $1, status = 'completed', content_type = $2 WHERE user_id = $3`, rel, tc.stored, user.ID); err != nil {
			t.Fatal(err)
		}

		req := httptest.NewRequest(http.MethodGet, "/rest/stream?id="+epID+"&apiKey="+testAPIKey+"&f=json&c=test&v=1.16.1", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("stored %q: status %d, want 200", tc.stored, rec.Code)
			continue
		}
		if got := rec.Header().Get("Content-Type"); got != tc.want {
			t.Errorf("stored %q: Content-Type %q, want %q", tc.stored, got, tc.want)
		}
	}

	// A stored active type on a non-media file stays refused.
	rel := filepath.Join("podcasts", db.UUIDString(user.ID), "e.html")
	_ = os.WriteFile(filepath.Join(h.storageRoot, rel), []byte("<script>"), 0o644)
	if _, err := pool.Exec(ctx, `UPDATE podcast_episodes SET disk_path = $1, content_type = 'text/html' WHERE user_id = $2`, rel, user.ID); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/rest/stream?id="+epID+"&apiKey="+testAPIKey+"&f=json&c=test&v=1.16.1", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		t.Errorf("text/html .html episode served as %q", rec.Header().Get("Content-Type"))
	}
}
