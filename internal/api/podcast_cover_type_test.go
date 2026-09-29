package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/db"
)

// A feed chooses its cover URL, so the cover's name must not decide the type it
// is served with on our origin: an HTML "cover" is never cached, a cached file
// is served with the type its bytes have, and always with nosniff.
func TestPodcastCoverTypeComesFromBytes(t *testing.T) {
	ctx := context.Background()
	q, svc, s := buildMusicServer(t)
	s.storageRoot = t.TempDir()
	usePodcastTestFeed(t)
	usePodcastTestCover(t)

	const feed = `<?xml version="1.0"?><rss version="2.0" xmlns:itunes="http://www.itunes.com/dtds/podcast-1.0.dtd">
<channel><title>Evil</title><itunes:image href="http://%s/cover.html"/>
<item><title>E1</title><enclosure url="http://example.com/e.mp3" type="audio/mpeg"/></item></channel></rss>`
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/cover.html" {
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, "<html><script>alert(document.domain)</script></html>")
			return
		}
		fmt.Fprintf(w, feed, srv.Listener.Addr().String())
	}))
	defer srv.Close()

	tok, user, err := svc.Register(ctx, "podtype@x.test", "password12")
	if err != nil {
		t.Fatal(err)
	}
	create := svc.Middleware(http.HandlerFunc(s.handleCreatePodcast))
	cover := svc.Middleware(http.HandlerFunc(s.handleGetPodcastCover))

	rec := httptest.NewRecorder()
	create.ServeHTTP(rec, authedReq(http.MethodPost, "/me/music/podcasts", tok, fmt.Sprintf(`{"url":%q}`, srv.URL), ""))
	if rec.Code != http.StatusCreated {
		t.Fatalf("subscribe: %d %s", rec.Code, rec.Body.String())
	}
	var ch podcastDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &ch)
	if ch.HasCover {
		t.Fatal("an HTML page was cached as the channel cover")
	}

	get := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := authedReq(http.MethodGet, "/me/music/podcasts/"+ch.ID+"/cover", tok, "", "")
		req.SetPathValue("id", ch.ID)
		cover.ServeHTTP(rec, req)
		return rec
	}
	setCover := func(name string, data []byte) {
		rel := filepath.Join("podcasts", db.UUIDString(user.ID), "covers", name)
		abs := filepath.Join(s.storageRoot, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, data, 0o644); err != nil {
			t.Fatal(err)
		}
		id, _ := db.ParseUUID(ch.ID)
		if err := q.SetPodcastChannelCoverPath(ctx, db.SetPodcastChannelCoverPathParams{
			ID: id, UserID: user.ID, CoverPath: pgtype.Text{String: rel, Valid: true},
		}); err != nil {
			t.Fatal(err)
		}
	}

	// A file cached before this fix, named after the feed's URL.
	setCover("old.html", []byte("<html><script>alert(1)</script></html>"))
	if rec := get(); rec.Code == http.StatusOK {
		t.Errorf("HTML cover served as %q", rec.Header().Get("Content-Type"))
	}

	var img bytes.Buffer
	_ = png.Encode(&img, image.NewRGBA(image.Rect(0, 0, 1, 1)))
	setCover("x.svg", img.Bytes())
	rec = get()
	if rec.Code != http.StatusOK {
		t.Fatalf("png cover: %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("Content-Type = %q, want image/png from the bytes", ct)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("nosniff missing")
	}
	if !bytes.Equal(rec.Body.Bytes(), img.Bytes()) || strings.Contains(rec.Body.String(), "html") {
		t.Error("body mismatch")
	}
}
