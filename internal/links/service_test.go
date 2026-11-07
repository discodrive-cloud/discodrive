package links

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"discodrive/internal/db"
)

// testService returns a Service whose fetches may reach httptest servers on 127.0.0.1.
func testService(q *db.Queries) *Service {
	s := NewService(q)
	s.Client = &http.Client{}
	s.Validate = func(string) error { return nil }
	return s
}

func page(head string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<html><head>" + head + "</head><body>x</body></html>"))
	}
}

func TestSaveThenEnrich(t *testing.T) {
	_, q, uid := bootstrap(t)
	ctx := context.Background()
	mux := http.NewServeMux()
	mux.HandleFunc("/img/p.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngOf(400, 200))
	})
	mux.HandleFunc("/post", page(`<meta property="og:title" content="Баклажаны с мисо"><meta name="author" content="@cook"><meta property="og:image" content="/img/p.png">`))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	s := testService(q)
	l, err := s.Save(ctx, uid, srv.URL+"/post?utm_source=x", "from share sheet")
	if err != nil {
		t.Fatal(err)
	}
	if l.Title != "from share sheet" || l.URL != srv.URL+"/post" || l.Source != "other" {
		t.Fatalf("saved %+v", l)
	}
	s.Wait()
	items, err := s.List(ctx, uid, false, "")
	if err != nil {
		t.Fatal(err)
	}
	got := items.Items[0]
	if got.Title != "from share sheet" || got.Author != "@cook" || !got.HasThumb {
		t.Fatalf("after enrich %+v", got)
	}
	thumb, err := q.GetSavedLinkThumb(ctx, db.GetSavedLinkThumbParams{ID: got.id, UserID: uid})
	if err != nil || len(thumb) == 0 || thumb[0] != 0xFF {
		t.Fatalf("thumb missing or not a JPEG: %v", err)
	}
}

func TestEnrichTakesPageTitleWhenNoneShared(t *testing.T) {
	_, q, uid := bootstrap(t)
	ctx := context.Background()
	srv := httptest.NewServer(page(`<title>Page title</title><meta property="og:image" content="//nowhere.invalid/x.png">`))
	defer srv.Close()
	s := testService(q)
	if _, err := s.Save(ctx, uid, srv.URL+"/", ""); err != nil {
		t.Fatal(err)
	}
	s.Wait()
	items, _ := s.List(ctx, uid, false, "")
	if l := items.Items[0]; l.Title != "Page title" || l.HasThumb {
		t.Fatalf("got %+v", l)
	}
}

func TestEnrichSkipsNonHTML(t *testing.T) {
	_, q, uid := bootstrap(t)
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`<head><title>should not be read</title></head>`))
	}))
	defer srv.Close()
	s := testService(q)
	if _, err := s.Save(ctx, uid, srv.URL+"/", ""); err != nil {
		t.Fatal(err)
	}
	s.Wait()
	items, _ := s.List(ctx, uid, false, "")
	if l := items.Items[0]; l.Title != "" || !l.enriched {
		t.Fatalf("got %+v", l)
	}
}

func TestEnrichReadsOnlyTheStartOfThePage(t *testing.T) {
	_, q, uid := bootstrap(t)
	ctx := context.Background()
	srv := httptest.NewServer(page(`<meta name="x" content="` + strings.Repeat("a", maxPageBytes) + `"><meta property="og:title" content="too far">`))
	defer srv.Close()
	s := testService(q)
	if _, err := s.Save(ctx, uid, srv.URL+"/", ""); err != nil {
		t.Fatal(err)
	}
	s.Wait()
	items, _ := s.List(ctx, uid, false, "")
	if l := items.Items[0]; l.Title != "" {
		t.Fatalf("metadata past the read limit must be ignored, got %q", l.Title)
	}
}

func TestListFiltersAndCounts(t *testing.T) {
	_, q, uid := bootstrap(t)
	ctx := context.Background()
	s := testService(q)
	s.noFetch = true
	a, _ := s.Save(ctx, uid, "https://www.instagram.com/reel/A/", "")
	_, _ = s.Save(ctx, uid, "https://www.reddit.com/r/homelab/1", "")
	if err := s.SetArchived(ctx, uid, a.ID, true); err != nil {
		t.Fatal(err)
	}
	inbox, _ := s.List(ctx, uid, false, "")
	if len(inbox.Items) != 1 || inbox.InboxCount != 1 || strings.Join(inbox.Sources, ",") != "reddit" {
		t.Fatalf("inbox %+v", inbox)
	}
	arch, _ := s.List(ctx, uid, true, "instagram")
	if len(arch.Items) != 1 || !arch.Items[0].Archived || arch.InboxCount != 1 {
		t.Fatalf("archive %+v", arch)
	}
	if none, _ := s.List(ctx, uid, true, "reddit"); len(none.Items) != 0 {
		t.Fatalf("source filter leaked %+v", none)
	}
	if err := s.Delete(ctx, uid, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, uid, a.ID); err != ErrNotFound {
		t.Fatalf("second delete err = %v, want ErrNotFound", err)
	}
}

func TestEnrichUsesOEmbedWhereThePageHidesMetadata(t *testing.T) {
	_, q, uid := bootstrap(t)
	ctx := context.Background()
	var pageHits atomic.Int32
	var gotUA, gotURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/oembed", func(w http.ResponseWriter, r *http.Request) {
		gotUA, gotURL = r.UserAgent(), r.URL.Query().Get("url")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"title":"My homelab diagram","author_name":"goblinmine","thumbnail_url":"/thumb.png"}`))
	})
	mux.HandleFunc("/thumb.png", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(pngOf(480, 360)) })
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { pageHits.Add(1) })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	s := testService(q)
	s.oembed = map[string]string{"reddit": srv.URL + "/oembed"}
	if _, err := s.Save(ctx, uid, "https://www.reddit.com/r/homelab/comments/1/x/", ""); err != nil {
		t.Fatal(err)
	}
	s.Wait()
	p, _ := s.List(ctx, uid, false, "")
	l := p.Items[0]
	if l.Title != "My homelab diagram" || l.Author != "goblinmine" || !l.HasThumb {
		t.Fatalf("got %+v", l)
	}
	if gotURL != "https://www.reddit.com/r/homelab/comments/1/x/" || !strings.Contains(gotUA, "DiscoDriveBot") {
		t.Fatalf("oembed asked url=%q ua=%q", gotURL, gotUA)
	}
	if pageHits.Load() != 0 {
		t.Fatal("the page itself must not be fetched when oEmbed answered")
	}
}

func TestEnrichFallsBackToThePageWhenOEmbedFails(t *testing.T) {
	_, q, uid := bootstrap(t)
	ctx := context.Background()
	mux := http.NewServeMux()
	mux.HandleFunc("/oembed", func(w http.ResponseWriter, r *http.Request) { http.Error(w, "no", http.StatusNotFound) })
	mux.HandleFunc("/", page(`<meta property="og:title" content="From the page">`))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	s := testService(q)
	s.oembed = map[string]string{"other": srv.URL + "/oembed"}
	if _, err := s.Save(ctx, uid, srv.URL+"/post", ""); err != nil {
		t.Fatal(err)
	}
	s.Wait()
	p, _ := s.List(ctx, uid, false, "")
	if p.Items[0].Title != "From the page" {
		t.Fatalf("got %+v", p.Items[0])
	}
}

func TestPageFetchIdentifiesAsLinkPreview(t *testing.T) {
	_, q, uid := bootstrap(t)
	var ua string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ua = r.UserAgent()
		page(`<title>t</title>`)(w, r)
	}))
	defer srv.Close()
	s := testService(q)
	if _, err := s.Save(context.Background(), uid, srv.URL+"/", ""); err != nil {
		t.Fatal(err)
	}
	s.Wait()
	if ua != UserAgent || !strings.Contains(ua, "like TwitterBot") {
		t.Fatalf("user agent %q", ua)
	}
}

// More links than fetch slots at once: every one still gets its preview without
// waiting for the worker tick.
func TestBurstOfLinksAllGetEnriched(t *testing.T) {
	_, q, uid := bootstrap(t)
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		page(`<title>`+r.URL.Path+`</title>`)(w, r)
	}))
	defer srv.Close()
	s := testService(q)
	for i := 0; i < 3*maxParallel; i++ {
		if _, err := s.Save(ctx, uid, fmt.Sprintf("%s/p%d", srv.URL, i), ""); err != nil {
			t.Fatal(err)
		}
	}
	s.Wait()
	p, _ := s.List(ctx, uid, false, "")
	for _, l := range p.Items {
		if l.Title == "" {
			t.Fatalf("%s was left for the tick", l.URL)
		}
	}
}
