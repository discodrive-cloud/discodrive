package subsonic

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"discodrive/internal/db"
)

// flakyRSSServer serves sampleRSS until down is set, then answers 503.
func flakyRSSServer(t *testing.T, down *atomic.Bool) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down.Load() {
			http.Error(w, "maintenance", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/rss+xml")
		fmt.Fprint(w, strings.ReplaceAll(sampleRSS, "%s", srv.Listener.Addr().String()))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// Re-adding a feed the user already follows while that feed is down must keep
// the subscription and the downloaded episode files (they count against quota
// and would be orphaned on disk if the row went away).
func TestCreatePodcastChannel_ExistingSurvivesFailedRefresh(t *testing.T) {
	restore := overrideFetchFeed()
	defer restore()

	h, ctx, pool := setupWithPool(t)
	h.storageRoot = t.TempDir()

	var down atomic.Bool
	srv := flakyRSSServer(t, &down)
	feed := "url=" + srv.URL + "/feed.xml"
	if resp := doGet(h, testAPIKey, "createPodcastChannel", feed); resp["status"] != "ok" {
		t.Fatalf("first subscribe: %v", resp)
	}

	// Pretend the episode was downloaded.
	user, err := h.q.GetUserByEmail(ctx, testEmail)
	if err != nil {
		t.Fatal(err)
	}
	rel := filepath.Join("podcasts", db.UUIDString(user.ID), "ep1.mp3")
	abs := filepath.Join(h.storageRoot, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte("AUDIO"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE podcast_episodes SET disk_path = $1, status = 'completed' WHERE user_id = $2`, rel, user.ID); err != nil {
		t.Fatal(err)
	}

	down.Store(true)
	resp := doGet(h, testAPIKey, "createPodcastChannel", feed)
	if resp["status"] != "ok" {
		t.Errorf("re-subscribe to a known feed while it is down: %v, want ok", resp)
	}

	chs, err := h.q.ListPodcastChannelsForUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(chs) != 1 {
		t.Fatalf("channels after failed refresh = %d, want the subscription kept", len(chs))
	}
	eps, err := h.q.ListEpisodesByChannel(ctx, chs[0].ID)
	if err != nil || len(eps) != 1 || !eps[0].DiskPath.Valid {
		t.Fatalf("episodes after failed refresh = %v (err %v), want the downloaded one kept", eps, err)
	}
	if _, err := os.Stat(abs); err != nil {
		t.Errorf("episode file: %v, want it kept", err)
	}
}

// A brand-new feed that cannot be fetched is not subscribed.
func TestCreatePodcastChannel_NewFeedFailureRollsBack(t *testing.T) {
	restore := overrideFetchFeed()
	defer restore()

	h, ctx, _ := setupWithPool(t)
	h.storageRoot = t.TempDir()

	var down atomic.Bool
	down.Store(true)
	srv := flakyRSSServer(t, &down)
	if resp := doGet(h, testAPIKey, "createPodcastChannel", "url="+srv.URL+"/feed.xml"); resp["status"] == "ok" {
		t.Fatalf("subscribe to a dead feed: %v, want failure", resp)
	}
	user, err := h.q.GetUserByEmail(ctx, testEmail)
	if err != nil {
		t.Fatal(err)
	}
	if chs, _ := h.q.ListPodcastChannelsForUser(ctx, user.ID); len(chs) != 0 {
		t.Errorf("channels = %d, want the new row rolled back", len(chs))
	}
}
