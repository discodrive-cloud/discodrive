package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"discodrive/internal/podcast"
)

// authedReq builds a JSON request with a Bearer token. For DELETE-by-id it sets
// the {id} path value the handlers read via r.PathValue("id").
func authedReq(method, path, bearer, body, pathID string) *http.Request {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, bytes.NewReader([]byte(body)))
		r.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	if pathID != "" {
		r.SetPathValue("id", pathID)
	}
	return r
}

func TestRadioCRUD(t *testing.T) {
	ctx := context.Background()
	_, svc, s := buildMusicServer(t)
	tok, _, err := svc.Register(ctx, "radio1@x.test", "password12")
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	create := svc.Middleware(http.HandlerFunc(s.handleCreateRadio))
	list := svc.Middleware(http.HandlerFunc(s.handleListRadio))
	del := svc.Middleware(http.HandlerFunc(s.handleDeleteRadio))

	// create
	rec := httptest.NewRecorder()
	create.ServeHTTP(rec, authedReq(http.MethodPost, "/me/music/radio", tok, `{"name":"Jazz","streamUrl":"http://x/s"}`, ""))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", rec.Code, rec.Body.String())
	}
	var created radioDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created: %v body=%s", err, rec.Body.String())
	}
	if created.ID == "" || created.Name != "Jazz" {
		t.Fatalf("unexpected created: %+v", created)
	}

	// list shows it
	rec = httptest.NewRecorder()
	list.ServeHTTP(rec, authedReq(http.MethodGet, "/me/music/radio", tok, "", ""))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Jazz") {
		t.Fatalf("list missing station: code=%d %s", rec.Code, rec.Body.String())
	}

	// delete
	rec = httptest.NewRecorder()
	del.ServeHTTP(rec, authedReq(http.MethodDelete, "/me/music/radio/"+created.ID, tok, "", created.ID))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete status=%d body=%s", rec.Code, rec.Body.String())
	}

	// list empty
	rec = httptest.NewRecorder()
	list.ServeHTTP(rec, authedReq(http.MethodGet, "/me/music/radio", tok, "", ""))
	if strings.Contains(rec.Body.String(), "Jazz") {
		t.Fatalf("station not deleted: %s", rec.Body.String())
	}

	// delete again → 404
	rec = httptest.NewRecorder()
	del.ServeHTTP(rec, authedReq(http.MethodDelete, "/me/music/radio/"+created.ID, tok, "", created.ID))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("re-delete status=%d, want 404", rec.Code)
	}
}

func TestRadioUpdate(t *testing.T) {
	ctx := context.Background()
	_, svc, s := buildMusicServer(t)
	tok, _, err := svc.Register(ctx, "radioupd@x.test", "password12")
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	create := svc.Middleware(http.HandlerFunc(s.handleCreateRadio))
	update := svc.Middleware(http.HandlerFunc(s.handleUpdateRadio))
	list := svc.Middleware(http.HandlerFunc(s.handleListRadio))

	// create a station
	rec := httptest.NewRecorder()
	create.ServeHTTP(rec, authedReq(http.MethodPost, "/me/music/radio", tok, `{"name":"Jazz","streamUrl":"http://x/s"}`, ""))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", rec.Code, rec.Body.String())
	}
	var created radioDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created: %v body=%s", err, rec.Body.String())
	}

	// update name + streamUrl
	rec = httptest.NewRecorder()
	update.ServeHTTP(rec, authedReq(http.MethodPut, "/me/music/radio/"+created.ID, tok, `{"name":"Blues","streamUrl":"http://x/s2"}`, created.ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("update status=%d body=%s", rec.Code, rec.Body.String())
	}

	// list shows the new name
	rec = httptest.NewRecorder()
	list.ServeHTTP(rec, authedReq(http.MethodGet, "/me/music/radio", tok, "", ""))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Blues") || strings.Contains(rec.Body.String(), "Jazz") {
		t.Fatalf("list after update: code=%d %s", rec.Code, rec.Body.String())
	}

	// empty name → 400
	rec = httptest.NewRecorder()
	update.ServeHTTP(rec, authedReq(http.MethodPut, "/me/music/radio/"+created.ID, tok, `{"name":"","streamUrl":"http://x/s2"}`, created.ID))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("empty name: status=%d, want 400", rec.Code)
	}

	// non-existent id → 404
	missing := "00000000-0000-0000-0000-000000000000"
	rec = httptest.NewRecorder()
	update.ServeHTTP(rec, authedReq(http.MethodPut, "/me/music/radio/"+missing, tok, `{"name":"X","streamUrl":"http://x/s"}`, missing))
	if rec.Code != http.StatusNotFound {
		t.Errorf("missing id: status=%d, want 404", rec.Code)
	}

	// isolation: user B cannot update A's station → 404
	tokB, _, err := svc.Register(ctx, "radioupdB@x.test", "password12")
	if err != nil {
		t.Fatalf("register B: %v", err)
	}
	rec = httptest.NewRecorder()
	update.ServeHTTP(rec, authedReq(http.MethodPut, "/me/music/radio/"+created.ID, tokB, `{"name":"Hijack","streamUrl":"http://x/s"}`, created.ID))
	if rec.Code != http.StatusNotFound {
		t.Errorf("B update A's station: status=%d, want 404", rec.Code)
	}
}

func TestRadioCreateValidation(t *testing.T) {
	ctx := context.Background()
	_, svc, s := buildMusicServer(t)
	tok, _, err := svc.Register(ctx, "radio2@x.test", "password12")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	create := svc.Middleware(http.HandlerFunc(s.handleCreateRadio))

	rec := httptest.NewRecorder()
	create.ServeHTTP(rec, authedReq(http.MethodPost, "/me/music/radio", tok, `{"name":"NoURL"}`, ""))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("missing streamUrl: status=%d, want 400", rec.Code)
	}

	rec = httptest.NewRecorder()
	create.ServeHTTP(rec, authedReq(http.MethodPost, "/me/music/radio", tok, `{"streamUrl":"http://x/s"}`, ""))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("missing name: status=%d, want 400", rec.Code)
	}
}

func TestRadioUserIsolation(t *testing.T) {
	ctx := context.Background()
	_, svc, s := buildMusicServer(t)
	tokA, _, err := svc.Register(ctx, "radioA@x.test", "password12")
	if err != nil {
		t.Fatalf("register A: %v", err)
	}
	tokB, _, err := svc.Register(ctx, "radioB@x.test", "password12")
	if err != nil {
		t.Fatalf("register B: %v", err)
	}

	create := svc.Middleware(http.HandlerFunc(s.handleCreateRadio))
	list := svc.Middleware(http.HandlerFunc(s.handleListRadio))
	del := svc.Middleware(http.HandlerFunc(s.handleDeleteRadio))

	// A creates a station
	rec := httptest.NewRecorder()
	create.ServeHTTP(rec, authedReq(http.MethodPost, "/me/music/radio", tokA, `{"name":"OnlyA","streamUrl":"http://x/s"}`, ""))
	if rec.Code != http.StatusCreated {
		t.Fatalf("A create: %d %s", rec.Code, rec.Body.String())
	}
	var st radioDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &st)

	// B cannot see it
	rec = httptest.NewRecorder()
	list.ServeHTTP(rec, authedReq(http.MethodGet, "/me/music/radio", tokB, "", ""))
	if strings.Contains(rec.Body.String(), "OnlyA") {
		t.Fatalf("B sees A's station: %s", rec.Body.String())
	}

	// B cannot delete it → 404
	rec = httptest.NewRecorder()
	del.ServeHTTP(rec, authedReq(http.MethodDelete, "/me/music/radio/"+st.ID, tokB, "", st.ID))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("B delete A's station: status=%d, want 404", rec.Code)
	}
}

// rssFeed is a minimal valid RSS 2.0 podcast feed.
const rssFeed = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
  <channel>
    <title>Test Cast</title>
    <description>A test podcast</description>
    <item>
      <title>Episode 1</title>
      <description>First episode</description>
      <enclosure url="http://example.com/ep1.mp3" type="audio/mpeg" length="123"/>
    </item>
  </channel>
</rss>`

// usePodcastTestFeed points podcast.FetchFeedFunc at the SSRF-free fetcher so the
// handler can reach a local httptest server. Restored on test cleanup.
func usePodcastTestFeed(t *testing.T) {
	t.Helper()
	orig := podcast.FetchFeedFunc
	podcast.FetchFeedFunc = func(ctx context.Context, feedURL string) (podcast.Feed, error) {
		return podcast.FetchFeedUnsafe(ctx, http.DefaultClient, feedURL)
	}
	t.Cleanup(func() { podcast.FetchFeedFunc = orig })
}

// usePodcastTestCover points podcast.CoverDownloadFunc at the SSRF-free downloader
// so the cover actually downloads from a local httptest server. Restored on cleanup.
func usePodcastTestCover(t *testing.T) {
	t.Helper()
	orig := podcast.CoverDownloadFunc
	podcast.CoverDownloadFunc = func(ctx context.Context, srcURL, destPath string) (int64, string, string, error) {
		return podcast.DownloadToUnsafe(ctx, http.DefaultClient, srcURL, destPath, podcast.MaxCoverBytes)
	}
	t.Cleanup(func() { podcast.CoverDownloadFunc = orig })
}

// rssFeedWithCover is an RSS feed whose channel carries an itunes:image. The %s is
// the httptest host so the cover URL resolves to the local server.
const rssFeedWithCover = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:itunes="http://www.itunes.com/dtds/podcast-1.0.dtd">
  <channel>
    <title>Covered Cast</title>
    <description>A test podcast with a cover</description>
    <itunes:image href="http://%s/cover.jpg"/>
    <item>
      <title>Episode 1</title>
      <description>First episode</description>
      <enclosure url="http://example.com/ep1.mp3" type="audio/mpeg" length="123"/>
    </item>
  </channel>
</rss>`

func TestPodcastCover(t *testing.T) {
	ctx := context.Background()
	_, svc, s := buildMusicServer(t)
	s.storageRoot = t.TempDir()
	usePodcastTestFeed(t)
	usePodcastTestCover(t)

	coverBytes := []byte("\xff\xd8\xffFAKEJPEGDATA")
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cover.jpg":
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write(coverBytes)
		default:
			w.Header().Set("Content-Type", "application/rss+xml")
			fmt.Fprintf(w, rssFeedWithCover, srv.Listener.Addr().String())
		}
	}))
	defer srv.Close()

	tok, _, err := svc.Register(ctx, "podcover@x.test", "password12")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	create := svc.Middleware(http.HandlerFunc(s.handleCreatePodcast))
	list := svc.Middleware(http.HandlerFunc(s.handleListPodcasts))
	cover := svc.Middleware(http.HandlerFunc(s.handleGetPodcastCover))

	// Subscribe.
	rec := httptest.NewRecorder()
	create.ServeHTTP(rec, authedReq(http.MethodPost, "/me/music/podcasts", tok, fmt.Sprintf(`{"url":%q}`, srv.URL), ""))
	if rec.Code != http.StatusCreated {
		t.Fatalf("subscribe status=%d body=%s", rec.Code, rec.Body.String())
	}

	// Read the channel id from the list; it should report hasCover true.
	rec = httptest.NewRecorder()
	list.ServeHTTP(rec, authedReq(http.MethodGet, "/me/music/podcasts", tok, "", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", rec.Code, rec.Body.String())
	}
	var channels []podcastDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &channels); err != nil {
		t.Fatalf("decode list: %v body=%s", err, rec.Body.String())
	}
	if len(channels) != 1 {
		t.Fatalf("expected 1 channel, got %d: %s", len(channels), rec.Body.String())
	}
	if !channels[0].HasCover {
		t.Fatalf("expected hasCover true, got %+v", channels[0])
	}
	id := channels[0].ID

	// GET the cover → 200 with the served bytes.
	rec = httptest.NewRecorder()
	req := authedReq(http.MethodGet, "/me/music/podcasts/"+id+"/cover", tok, "", "")
	req.SetPathValue("id", id)
	cover.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("cover status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !bytes.Equal(rec.Body.Bytes(), coverBytes) {
		t.Fatalf("cover body mismatch: got %q want %q", rec.Body.Bytes(), coverBytes)
	}

	// Unknown id → 404.
	missing := "00000000-0000-0000-0000-000000000000"
	rec = httptest.NewRecorder()
	req = authedReq(http.MethodGet, "/me/music/podcasts/"+missing+"/cover", tok, "", "")
	req.SetPathValue("id", missing)
	cover.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing cover status=%d, want 404", rec.Code)
	}
}

func TestPodcastSubscribe(t *testing.T) {
	ctx := context.Background()
	_, svc, s := buildMusicServer(t)
	s.storageRoot = t.TempDir()
	usePodcastTestFeed(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		fmt.Fprint(w, rssFeed)
	}))
	defer srv.Close()

	tok, _, err := svc.Register(ctx, "pod1@x.test", "password12")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	create := svc.Middleware(http.HandlerFunc(s.handleCreatePodcast))
	list := svc.Middleware(http.HandlerFunc(s.handleListPodcasts))

	rec := httptest.NewRecorder()
	create.ServeHTTP(rec, authedReq(http.MethodPost, "/me/music/podcasts", tok, fmt.Sprintf(`{"url":%q}`, srv.URL), ""))
	if rec.Code != http.StatusCreated {
		t.Fatalf("subscribe status=%d body=%s", rec.Code, rec.Body.String())
	}
	var ch podcastDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &ch); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	if ch.Title != "Test Cast" {
		t.Fatalf("expected populated title, got %+v", ch)
	}

	rec = httptest.NewRecorder()
	list.ServeHTTP(rec, authedReq(http.MethodGet, "/me/music/podcasts", tok, "", ""))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Test Cast") {
		t.Fatalf("list missing channel: code=%d %s", rec.Code, rec.Body.String())
	}
}

func TestPodcastSubscribeBadFeed(t *testing.T) {
	ctx := context.Background()
	_, svc, s := buildMusicServer(t)
	s.storageRoot = t.TempDir()
	usePodcastTestFeed(t)

	// Server that returns garbage so the feed parse fails.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer srv.Close()

	tok, _, err := svc.Register(ctx, "pod2@x.test", "password12")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	create := svc.Middleware(http.HandlerFunc(s.handleCreatePodcast))
	list := svc.Middleware(http.HandlerFunc(s.handleListPodcasts))

	rec := httptest.NewRecorder()
	create.ServeHTTP(rec, authedReq(http.MethodPost, "/me/music/podcasts", tok, fmt.Sprintf(`{"url":%q}`, srv.URL), ""))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad feed status=%d body=%s, want 400", rec.Code, rec.Body.String())
	}

	// Channel must not be left behind.
	rec = httptest.NewRecorder()
	list.ServeHTTP(rec, authedReq(http.MethodGet, "/me/music/podcasts", tok, "", ""))
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), srv.URL) {
		t.Fatalf("rolled-back channel left behind: %s", rec.Body.String())
	}
}

// TestPodcastResubscribeKeepsExistingOnFailure guards the data-loss bug: when a
// user re-subscribes to a feed they already have and the feed fetch fails, the
// existing subscription must survive (no rollback delete of a pre-existing row).
func TestPodcastResubscribeKeepsExistingOnFailure(t *testing.T) {
	ctx := context.Background()
	_, svc, s := buildMusicServer(t)
	s.storageRoot = t.TempDir()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		fmt.Fprint(w, rssFeed)
	}))
	defer srv.Close()

	tok, _, err := svc.Register(ctx, "podresub@x.test", "password12")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	create := svc.Middleware(http.HandlerFunc(s.handleCreatePodcast))
	list := svc.Middleware(http.HandlerFunc(s.handleListPodcasts))

	// Working feed: first subscribe succeeds.
	orig := podcast.FetchFeedFunc
	podcast.FetchFeedFunc = func(ctx context.Context, feedURL string) (podcast.Feed, error) {
		return podcast.FetchFeedUnsafe(ctx, http.DefaultClient, feedURL)
	}
	defer func() { podcast.FetchFeedFunc = orig }()

	rec := httptest.NewRecorder()
	create.ServeHTTP(rec, authedReq(http.MethodPost, "/me/music/podcasts", tok, fmt.Sprintf(`{"url":%q}`, srv.URL), ""))
	if rec.Code != http.StatusCreated {
		t.Fatalf("first subscribe status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	list.ServeHTTP(rec, authedReq(http.MethodGet, "/me/music/podcasts", tok, "", ""))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), srv.URL) {
		t.Fatalf("channel not listed after subscribe: %s", rec.Body.String())
	}

	// Now simulate the feed being temporarily down and re-subscribe to the SAME url.
	podcast.FetchFeedFunc = func(ctx context.Context, feedURL string) (podcast.Feed, error) {
		return podcast.Feed{}, fmt.Errorf("feed down")
	}
	rec = httptest.NewRecorder()
	create.ServeHTTP(rec, authedReq(http.MethodPost, "/me/music/podcasts", tok, fmt.Sprintf(`{"url":%q}`, srv.URL), ""))

	// The existing subscription must NOT be deleted by the failed refresh.
	rec = httptest.NewRecorder()
	list.ServeHTTP(rec, authedReq(http.MethodGet, "/me/music/podcasts", tok, "", ""))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), srv.URL) {
		t.Fatalf("re-subscribe with down feed destroyed existing subscription: %s", rec.Body.String())
	}
}

func TestPodcastUserIsolation(t *testing.T) {
	ctx := context.Background()
	_, svc, s := buildMusicServer(t)
	s.storageRoot = t.TempDir()
	usePodcastTestFeed(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, rssFeed)
	}))
	defer srv.Close()

	tokA, _, err := svc.Register(ctx, "podA@x.test", "password12")
	if err != nil {
		t.Fatalf("register A: %v", err)
	}
	tokB, _, err := svc.Register(ctx, "podB@x.test", "password12")
	if err != nil {
		t.Fatalf("register B: %v", err)
	}
	create := svc.Middleware(http.HandlerFunc(s.handleCreatePodcast))
	list := svc.Middleware(http.HandlerFunc(s.handleListPodcasts))
	del := svc.Middleware(http.HandlerFunc(s.handleDeletePodcast))

	rec := httptest.NewRecorder()
	create.ServeHTTP(rec, authedReq(http.MethodPost, "/me/music/podcasts", tokA, fmt.Sprintf(`{"url":%q}`, srv.URL), ""))
	if rec.Code != http.StatusCreated {
		t.Fatalf("A subscribe: %d %s", rec.Code, rec.Body.String())
	}
	var ch podcastDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &ch)

	// B cannot list A's channel.
	rec = httptest.NewRecorder()
	list.ServeHTTP(rec, authedReq(http.MethodGet, "/me/music/podcasts", tokB, "", ""))
	if strings.Contains(rec.Body.String(), "Test Cast") {
		t.Fatalf("B sees A's podcast: %s", rec.Body.String())
	}

	// B cannot delete A's channel → 404.
	rec = httptest.NewRecorder()
	del.ServeHTTP(rec, authedReq(http.MethodDelete, "/me/music/podcasts/"+ch.ID, tokB, "", ch.ID))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("B delete A's podcast: status=%d, want 404", rec.Code)
	}
}
