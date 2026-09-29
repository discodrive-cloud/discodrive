package subsonic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"discodrive/internal/db"
	"discodrive/internal/podcast"
	"discodrive/internal/secret"

	"github.com/jackc/pgx/v5/pgtype"
)

// sampleRSS is a minimal RSS feed with one episode.
const sampleRSS = `<?xml version="1.0"?>
<rss version="2.0" xmlns:itunes="http://www.itunes.com/dtds/podcast-1.0.dtd">
<channel>
  <title>Test Cast</title>
  <description>A test podcast</description>
  <item>
    <title>Ep 1</title>
    <description>first episode</description>
    <enclosure url="http://%s/ep1.mp3" type="audio/mpeg" length="123"/>
  </item>
</channel></rss>`

// startRSSServer spins up an httptest server that serves one RSS feed and one episode file.
// Returns the server and a cleanup function.
func startRSSServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/feed.xml", func(w http.ResponseWriter, r *http.Request) {
		host := srv.Listener.Addr().String()
		body := strings.ReplaceAll(sampleRSS, "%s", host)
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = fmt.Fprint(w, body)
	})
	mux.HandleFunc("/ep1.mp3", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write([]byte("FAKEAUDIO"))
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// overrideFetchFeed replaces the podcast.FetchFeedFunc seam with a version that
// uses http.DefaultClient (bypasses SSRF guard so httptest URLs work).
// Returns a restore function to be deferred.
func overrideFetchFeed() func() {
	orig := podcast.FetchFeedFunc
	podcast.FetchFeedFunc = func(ctx context.Context, url string) (podcast.Feed, error) {
		return podcast.FetchFeedUnsafe(ctx, http.DefaultClient, url)
	}
	return func() { podcast.FetchFeedFunc = orig }
}

// TestCreateAndGetPodcasts creates a channel and verifies it appears in getPodcasts.
func TestCreateAndGetPodcasts(t *testing.T) {
	restore := overrideFetchFeed()
	defer restore()

	h, _, _ := setupWithPool(t)
	h.storageRoot = t.TempDir()

	srv := startRSSServer(t)

	// createPodcastChannel
	resp := doGet(h, testAPIKey, "createPodcastChannel", "url="+srv.URL+"/feed.xml")
	if resp == nil || resp["status"] != "ok" {
		t.Fatalf("createPodcastChannel failed: %v", resp)
	}

	// getPodcasts should show the channel
	resp = doGet(h, testAPIKey, "getPodcasts", "")
	if resp == nil || resp["status"] != "ok" {
		t.Fatalf("getPodcasts failed: %v", resp)
	}

	podcasts, _ := resp["podcasts"].(map[string]any)
	channels, _ := podcasts["channel"].([]any)
	if len(channels) != 1 {
		t.Fatalf("expected 1 channel, got %d", len(channels))
	}

	ch, _ := channels[0].(map[string]any)
	if ch["title"] != "Test Cast" {
		t.Errorf("channel title = %q, want Test Cast", ch["title"])
	}
	chID, _ := ch["id"].(string)
	if !strings.HasPrefix(chID, "pc-") {
		t.Errorf("channel id should start with pc-, got %q", chID)
	}

	episodes, _ := ch["episode"].([]any)
	if len(episodes) != 1 {
		t.Fatalf("expected 1 episode, got %d", len(episodes))
	}

	ep, _ := episodes[0].(map[string]any)
	epID, _ := ep["id"].(string)
	if !strings.HasPrefix(epID, "pe-") {
		t.Errorf("episode id should start with pe-, got %q", epID)
	}
	if ep["title"] != "Ep 1" {
		t.Errorf("episode title = %q, want Ep 1", ep["title"])
	}
}

// TestGetNewestPodcasts creates a channel (which populates episodes) and verifies
// getNewestPodcasts returns at least one episode.
func TestGetNewestPodcasts(t *testing.T) {
	restore := overrideFetchFeed()
	defer restore()

	h, _, _ := setupWithPool(t)
	h.storageRoot = t.TempDir()

	srv := startRSSServer(t)

	resp := doGet(h, testAPIKey, "createPodcastChannel", "url="+srv.URL+"/feed.xml")
	if resp == nil || resp["status"] != "ok" {
		t.Fatalf("createPodcastChannel failed: %v", resp)
	}

	resp = doGet(h, testAPIKey, "getNewestPodcasts", "")
	if resp == nil || resp["status"] != "ok" {
		t.Fatalf("getNewestPodcasts failed: %v", resp)
	}

	newest, _ := resp["newestPodcasts"].(map[string]any)
	episodes, _ := newest["episode"].([]any)
	if len(episodes) < 1 {
		t.Fatalf("expected at least 1 newest episode, got %d", len(episodes))
	}
}

// TestGetPodcastsNoEpisodes verifies that getPodcasts with includeEpisodes=false
// omits the episode list from each channel object.
func TestGetPodcastsNoEpisodes(t *testing.T) {
	restore := overrideFetchFeed()
	defer restore()

	h, _, _ := setupWithPool(t)
	h.storageRoot = t.TempDir()

	srv := startRSSServer(t)

	resp := doGet(h, testAPIKey, "createPodcastChannel", "url="+srv.URL+"/feed.xml")
	if resp == nil || resp["status"] != "ok" {
		t.Fatalf("createPodcastChannel failed: %v", resp)
	}

	resp = doGet(h, testAPIKey, "getPodcasts", "includeEpisodes=false")
	if resp == nil || resp["status"] != "ok" {
		t.Fatalf("getPodcasts failed: %v", resp)
	}

	podcasts, _ := resp["podcasts"].(map[string]any)
	channels, _ := podcasts["channel"].([]any)
	if len(channels) != 1 {
		t.Fatalf("expected 1 channel, got %d", len(channels))
	}
	ch, _ := channels[0].(map[string]any)
	if eps, ok := ch["episode"]; ok {
		if list, _ := eps.([]any); len(list) != 0 {
			t.Errorf("expected no episodes with includeEpisodes=false, got %d", len(list))
		}
	}
}

// TestGetPodcastsEmpty verifies that getPodcasts returns ok with an empty channel list
// when the user has no subscriptions.
func TestGetPodcastsEmpty(t *testing.T) {
	h, _, _ := setupWithPool(t) //nolint
	h.storageRoot = t.TempDir()

	resp := doGet(h, testAPIKey, "getPodcasts", "")
	if resp == nil || resp["status"] != "ok" {
		t.Fatalf("getPodcasts failed: %v", resp)
	}

	podcasts, _ := resp["podcasts"].(map[string]any)
	channels, _ := podcasts["channel"].([]any)
	if len(channels) != 0 {
		t.Errorf("expected 0 channels, got %d", len(channels))
	}
}

// TestDeletePodcastChannel creates a channel, deletes it, and verifies it is gone.
func TestDeletePodcastChannel(t *testing.T) {
	restore := overrideFetchFeed()
	defer restore()

	h, ctx, _ := setupWithPool(t)
	h.storageRoot = t.TempDir()

	srv := startRSSServer(t)

	// Create
	resp := doGet(h, testAPIKey, "createPodcastChannel", "url="+srv.URL+"/feed.xml")
	if resp == nil || resp["status"] != "ok" {
		t.Fatalf("createPodcastChannel failed: %v", resp)
	}

	// Get channel id
	resp = doGet(h, testAPIKey, "getPodcasts", "")
	podcasts, _ := resp["podcasts"].(map[string]any)
	channels, _ := podcasts["channel"].([]any)
	if len(channels) != 1 {
		t.Fatalf("expected 1 channel, got %d", len(channels))
	}
	ch, _ := channels[0].(map[string]any)
	chID, _ := ch["id"].(string)

	// Delete
	resp = doGet(h, testAPIKey, "deletePodcastChannel", "id="+chID)
	if resp == nil || resp["status"] != "ok" {
		t.Fatalf("deletePodcastChannel failed: %v", resp)
	}

	// Verify gone
	resp = doGet(h, testAPIKey, "getPodcasts", "")
	podcasts, _ = resp["podcasts"].(map[string]any)
	channels, _ = podcasts["channel"].([]any)
	if len(channels) != 0 {
		t.Errorf("expected 0 channels after delete, got %d", len(channels))
	}

	_ = ctx // used by setupWithPool
}

// TestPodcastUserIsolation verifies per-user scoping:
// A creates a channel; B sees none; B cannot delete A's channel; A still has it.
func TestPodcastUserIsolation(t *testing.T) {
	restore := overrideFetchFeed()
	defer restore()

	h, ctx, _ := setupWithPool(t)
	h.storageRoot = t.TempDir()

	srv := startRSSServer(t)

	// Create user B with their own API key.
	uA, err := h.q.GetUserByEmail(ctx, testEmail)
	if err != nil {
		t.Fatalf("GetUserByEmail(A): %v", err)
	}
	uB, err := h.q.CreateUser(ctx, db.CreateUserParams{
		TenantID:     uA.TenantID,
		Email:        "b-podcast@x.test",
		PasswordHash: "irrelevant",
		Role:         "user",
	})
	if err != nil {
		t.Fatalf("CreateUser(B): %v", err)
	}
	cipher, err := secret.New(testKey)
	if err != nil {
		t.Fatalf("secret.New: %v", err)
	}
	ct, err := cipher.Encrypt("passB-podcast")
	if err != nil {
		t.Fatalf("cipher.Encrypt(B): %v", err)
	}
	if _, err = h.q.UpsertMusicSettings(ctx, db.UpsertMusicSettingsParams{
		UserID:  uB.ID,
		Enabled: true,
	}); err != nil {
		t.Fatalf("UpsertMusicSettings(B): %v", err)
	}
	if err = h.q.SetMusicCredentials(ctx, db.SetMusicCredentialsParams{
		UserID:         uB.ID,
		PasswordCipher: pgtype.Text{String: ct, Valid: true},
		ApiKey:         pgtype.Text{String: "apikeyB-podcast", Valid: true},
	}); err != nil {
		t.Fatalf("SetMusicCredentials(B): %v", err)
	}
	const apikeyB = "apikeyB-podcast"

	// A creates a channel
	resp := doGet(h, testAPIKey, "createPodcastChannel", "url="+srv.URL+"/feed.xml")
	if resp == nil || resp["status"] != "ok" {
		t.Fatalf("A createPodcastChannel failed: %v", resp)
	}

	// Get A's channel id
	resp = doGet(h, testAPIKey, "getPodcasts", "")
	podcasts, _ := resp["podcasts"].(map[string]any)
	channels, _ := podcasts["channel"].([]any)
	if len(channels) != 1 {
		t.Fatalf("A expected 1 channel, got %d", len(channels))
	}
	ch, _ := channels[0].(map[string]any)
	aChannelID, _ := ch["id"].(string)

	// B sees no channels
	resp = doGet(h, apikeyB, "getPodcasts", "")
	if resp == nil || resp["status"] != "ok" {
		t.Fatalf("B getPodcasts failed: %v", resp)
	}
	podcasts, _ = resp["podcasts"].(map[string]any)
	channels, _ = podcasts["channel"].([]any)
	if len(channels) != 0 {
		t.Errorf("B expected 0 channels, got %d", len(channels))
	}

	// B tries to delete A's channel — should fail (not found for B)
	resp = doGet(h, apikeyB, "deletePodcastChannel", "id="+aChannelID)
	if resp == nil {
		t.Fatal("deletePodcastChannel returned nil")
	}
	if resp["status"] == "ok" {
		t.Error("B should not be able to delete A's channel")
	}

	// A still has the channel
	resp = doGet(h, testAPIKey, "getPodcasts", "")
	podcasts, _ = resp["podcasts"].(map[string]any)
	channels, _ = podcasts["channel"].([]any)
	if len(channels) != 1 {
		t.Errorf("A expected 1 channel after B's failed delete, got %d", len(channels))
	}
}

// overrideDownloadTo replaces the downloadTo seam with a version that uses
// http.DefaultClient (bypasses SSRF guard so httptest URLs work).
// Returns a restore function to be deferred.
func overrideDownloadTo() func() {
	orig := downloadTo
	downloadTo = func(ctx context.Context, url, dest string, max int64) (int64, string, string, error) {
		return podcast.DownloadToUnsafe(ctx, http.DefaultClient, url, dest, max)
	}
	return func() { downloadTo = orig }
}

// overrideProxyEpisode replaces the proxyEpisode seam with a version that uses
// http.DefaultClient (bypasses SSRF guard so httptest URLs work).
// Returns a restore function to be deferred.
func overrideProxyEpisode() func() {
	orig := proxyEpisode
	proxyEpisode = func(ctx context.Context, w http.ResponseWriter, r *http.Request, url string) (bool, error) {
		return podcast.ProxyStreamUnsafe(ctx, http.DefaultClient, w, r, url)
	}
	return func() { proxyEpisode = orig }
}

// fakeEpisodeAudio is the payload served by the test HTTP server as the episode file.
var fakeEpisodeAudio = []byte("FAKEAUDIO")

// getEpisodeID extracts the first episode id from a getPodcasts response.
func getEpisodeID(t *testing.T, h *Handler) string {
	t.Helper()
	resp := doGet(h, testAPIKey, "getPodcasts", "")
	if resp == nil || resp["status"] != "ok" {
		t.Fatalf("getPodcasts failed: %v", resp)
	}
	podcasts, _ := resp["podcasts"].(map[string]any)
	channels, _ := podcasts["channel"].([]any)
	if len(channels) == 0 {
		t.Fatal("no channels returned by getPodcasts")
	}
	ch, _ := channels[0].(map[string]any)
	episodes, _ := ch["episode"].([]any)
	if len(episodes) == 0 {
		t.Fatal("no episodes in channel")
	}
	ep, _ := episodes[0].(map[string]any)
	id, _ := ep["id"].(string)
	return id
}

// TestDownloadAndStreamEpisode verifies the full download + stream flow for a podcast episode.
func TestDownloadAndStreamEpisode(t *testing.T) {
	restoreFetch := overrideFetchFeed()
	defer restoreFetch()
	restoreDownload := overrideDownloadTo()
	defer restoreDownload()

	h, ctx, _ := setupWithPool(t)
	h.storageRoot = t.TempDir()
	h.xaccel = false

	srv := startRSSServer(t)

	// Create a podcast channel; this populates the episode.
	resp := doGet(h, testAPIKey, "createPodcastChannel", "url="+srv.URL+"/feed.xml")
	if resp == nil || resp["status"] != "ok" {
		t.Fatalf("createPodcastChannel failed: %v", resp)
	}

	epID := getEpisodeID(t, h)
	if !strings.HasPrefix(epID, "pe-") {
		t.Fatalf("episode id should start with pe-, got %q", epID)
	}

	// Trigger the download (async goroutine).
	resp = doGet(h, testAPIKey, "downloadPodcastEpisode", "id="+epID)
	if resp == nil || resp["status"] != "ok" {
		t.Fatalf("downloadPodcastEpisode failed: %v", resp)
	}

	// Poll the DB until the episode is actually downloaded to disk (up to ~10s).
	// The client-facing getPodcasts status is always "completed" now (episodes are
	// playable via proxy), so it can no longer signal real download completion —
	// poll the persisted disk_path/status directly instead.
	user, err := h.q.GetUserByEmail(ctx, testEmail)
	if err != nil {
		t.Fatalf("GetUserByEmail: %v", err)
	}
	_, epUUIDStr, ok := decID(epID)
	if !ok {
		t.Fatalf("decID(%q) failed", epID)
	}
	epUUID, err := db.ParseUUID(epUUIDStr)
	if err != nil {
		t.Fatalf("ParseUUID(%q): %v", epUUIDStr, err)
	}
	deadline := time.Now().Add(10 * time.Second)
	completed := false
	for time.Now().Before(deadline) {
		ep, err := h.q.GetEpisodeForUser(ctx, db.GetEpisodeForUserParams{ID: epUUID, UserID: user.ID})
		if err == nil && ep.Status == "completed" && ep.DiskPath.Valid {
			completed = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !completed {
		t.Fatal("episode did not reach status=completed within 10s")
	}

	// Stream the episode via GET /rest/stream?id=pe-<uuid>.
	target := "/rest/stream?id=" + epID + "&apiKey=" + testAPIKey + "&f=json&c=test&v=1.16.1"
	req := httptest.NewRequest(http.MethodGet, target, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d — body: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.Bytes(); !bytes.Equal(got, fakeEpisodeAudio) {
		t.Errorf("body mismatch: got %d bytes (%q), want %d bytes (%q)",
			len(got), got, len(fakeEpisodeAudio), fakeEpisodeAudio)
	}
}

// sampleRSSWithCover is a minimal RSS feed with one episode and a channel cover image.
const sampleRSSWithCover = `<?xml version="1.0"?>
<rss version="2.0" xmlns:itunes="http://www.itunes.com/dtds/podcast-1.0.dtd">
<channel>
  <title>Test Cast</title>
  <description>A test podcast</description>
  <itunes:image href="http://%s/cover.jpg"/>
  <item>
    <title>Ep 1</title>
    <description>first episode</description>
    <enclosure url="http://%s/ep1.mp3" type="audio/mpeg" length="123"/>
  </item>
</channel></rss>`

// fakeCoverBytes is the payload served by the test HTTP server as the cover image.
var fakeCoverBytes = []byte("\xff\xd8\xffFAKECOVERIMAGE") // JPEG magic: covers are sniffed

// startRSSServerWithCover spins up an httptest server serving an RSS feed whose
// channel has an itunes:image, plus the cover and episode files.
func startRSSServerWithCover(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/feed.xml", func(w http.ResponseWriter, r *http.Request) {
		host := srv.Listener.Addr().String()
		body := strings.ReplaceAll(sampleRSSWithCover, "%s", host)
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = fmt.Fprint(w, body)
	})
	mux.HandleFunc("/cover.jpg", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(fakeCoverBytes)
	})
	mux.HandleFunc("/ep1.mp3", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write([]byte("FAKEAUDIO"))
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// overrideDownloadCover replaces the podcast.CoverDownloadFunc seam with a version
// that uses http.DefaultClient (bypasses SSRF guard so httptest URLs work).
// Returns a restore function to be deferred.
func overrideDownloadCover() func() {
	orig := podcast.CoverDownloadFunc
	podcast.CoverDownloadFunc = func(ctx context.Context, url, dest string) (int64, string, string, error) {
		return podcast.DownloadToUnsafe(ctx, http.DefaultClient, url, dest, 0)
	}
	return func() { podcast.CoverDownloadFunc = orig }
}

// TestPodcastCoverArt verifies that a channel cover is cached on create and served
// via getCoverArt?id=pc-<channelUUID>.
func TestPodcastCoverArt(t *testing.T) {
	restoreFetch := overrideFetchFeed()
	defer restoreFetch()
	restoreCover := overrideDownloadCover()
	defer restoreCover()

	h, _, _ := setupWithPool(t)
	h.storageRoot = t.TempDir()
	h.xaccel = false

	srv := startRSSServerWithCover(t)

	// Create a channel; cacheChannelCover runs synchronously within refreshOneChannel.
	resp := doGet(h, testAPIKey, "createPodcastChannel", "url="+srv.URL+"/feed.xml")
	if resp == nil || resp["status"] != "ok" {
		t.Fatalf("createPodcastChannel failed: %v", resp)
	}

	// Find the channel id (pc-<uuid>).
	resp = doGet(h, testAPIKey, "getPodcasts", "")
	podcasts, _ := resp["podcasts"].(map[string]any)
	channels, _ := podcasts["channel"].([]any)
	if len(channels) != 1 {
		t.Fatalf("expected 1 channel, got %d", len(channels))
	}
	ch, _ := channels[0].(map[string]any)
	chID, _ := ch["id"].(string)
	if !strings.HasPrefix(chID, "pc-") {
		t.Fatalf("channel id should start with pc-, got %q", chID)
	}

	// getCoverArt should return the cover bytes.
	target := "/rest/getCoverArt?id=" + chID + "&apiKey=" + testAPIKey + "&c=test&v=1.16.1"
	req := httptest.NewRequest(http.MethodGet, target, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d — body: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.Bytes(); !bytes.Equal(got, fakeCoverBytes) {
		t.Errorf("body mismatch: got %d bytes (%q), want %d bytes (%q)",
			len(got), got, len(fakeCoverBytes), fakeCoverBytes)
	}
}

// TestStreamEpisodeProxies verifies that streaming a pe- id whose episode is NOT
// downloaded proxies the original audio URL through the server, so the client
// receives real audio bytes (not a Subsonic error envelope).
func TestStreamEpisodeProxies(t *testing.T) {
	restoreFetch := overrideFetchFeed()
	defer restoreFetch()
	restoreProxy := overrideProxyEpisode()
	defer restoreProxy()

	h, _, _ := setupWithPool(t)
	h.storageRoot = t.TempDir()
	h.xaccel = false

	srv := startRSSServer(t)

	// Create channel (populates episode with status "new"); audio_url points at
	// the local httptest server's /ep1.mp3.
	resp := doGet(h, testAPIKey, "createPodcastChannel", "url="+srv.URL+"/feed.xml")
	if resp == nil || resp["status"] != "ok" {
		t.Fatalf("createPodcastChannel failed: %v", resp)
	}

	epID := getEpisodeID(t, h)
	if !strings.HasPrefix(epID, "pe-") {
		t.Fatalf("episode id should start with pe-, got %q", epID)
	}

	// Stream without downloading — expect the audio bytes via proxy.
	target := "/rest/stream?id=" + epID + "&apiKey=" + testAPIKey + "&f=json&c=test&v=1.16.1"
	req := httptest.NewRequest(http.MethodGet, target, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d — body: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.Bytes(); !bytes.Equal(got, fakeEpisodeAudio) {
		t.Errorf("body mismatch: got %d bytes (%q), want %d bytes (%q)",
			len(got), got, len(fakeEpisodeAudio), fakeEpisodeAudio)
	}
}

// TestStreamEpisodeNotDownloaded verifies that when an episode is not downloaded
// AND the proxy fails (e.g. SSRF guard blocks the audio_url), streaming returns
// an error response (not partial/garbage audio). Here the proxyEpisode seam is
// left at its real SSRF-guarded implementation, which rejects the loopback
// httptest audio_url — exercising the proxy-failure path.
func TestStreamEpisodeNotDownloaded(t *testing.T) {
	restoreFetch := overrideFetchFeed()
	defer restoreFetch()

	h, _, _ := setupWithPool(t)
	h.storageRoot = t.TempDir()
	h.xaccel = false

	srv := startRSSServer(t)

	// Create channel (populates episode with status "new").
	resp := doGet(h, testAPIKey, "createPodcastChannel", "url="+srv.URL+"/feed.xml")
	if resp == nil || resp["status"] != "ok" {
		t.Fatalf("createPodcastChannel failed: %v", resp)
	}

	epID := getEpisodeID(t, h)
	if !strings.HasPrefix(epID, "pe-") {
		t.Fatalf("episode id should start with pe-, got %q", epID)
	}

	// Stream without downloading; the SSRF guard blocks the loopback audio_url,
	// so proxy fails — expect an error response.
	target := "/rest/stream?id=" + epID + "&apiKey=" + testAPIKey + "&f=json&c=test&v=1.16.1"
	req := httptest.NewRequest(http.MethodGet, target, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	// Should NOT return the audio bytes.
	if bytes.Equal(rec.Body.Bytes(), fakeEpisodeAudio) {
		t.Fatal("stream returned audio for a non-downloaded episode")
	}

	// Either a non-200 HTTP status, or a Subsonic error JSON.
	if rec.Code == http.StatusOK {
		// If 200, it must be a Subsonic-level error (status: failed).
		var m map[string]any
		if jsonErr := json.Unmarshal(rec.Body.Bytes(), &m); jsonErr != nil {
			t.Fatalf("unexpected 200 with non-JSON body: %s", rec.Body.String())
		}
		inner := subsonicResponse(m)
		if inner == nil || inner["status"] == "ok" {
			t.Fatalf("expected subsonic error for not-downloaded episode, got: %s", rec.Body.String())
		}
	}
}
