package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"discodrive/internal/db"
	"discodrive/internal/links"
)

func linksReq(h http.Handler, method, path, tok string, body any) *httptest.ResponseRecorder {
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rd)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func linksServer(t *testing.T) (http.Handler, string, string, *db.Queries) {
	h, tok, other, q, _ := linksServerWith(t)
	return h, tok, other, q
}

func linksServerWith(t *testing.T) (http.Handler, string, string, *db.Queries, *Server) {
	t.Helper()
	ctx := context.Background()
	_, q, svc := bootstrapPairingDB(t)
	tok, _, err := svc.Register(ctx, "a@x.test", "password12")
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := svc.Register(ctx, "b@x.test", "password12")
	if err != nil {
		t.Fatal(err)
	}
	ls := links.NewService(q)
	ls.Validate = func(string) error { return nil }
	ls.Client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, http.ErrHandlerTimeout // no network in tests: links stay without previews
	})}
	s := &Server{auth: svc, q: q, links: ls, linkLimiter: newLinkLimiter()}
	mux := http.NewServeMux()
	s.registerLinkRoutes(mux, func(h http.Handler) http.Handler { return svc.Middleware(h) })
	return mux, tok, other, q, s
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func decodePage(t *testing.T, rec *httptest.ResponseRecorder) links.Page {
	t.Helper()
	var p links.Page
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return p
}

func TestLinksCRUD(t *testing.T) {
	h, tok, _, _ := linksServer(t)
	rec := linksReq(h, "POST", "/me/links", tok, map[string]any{"url": "https://www.instagram.com/reel/A/?igsh=1", "title": "Рецепт"})
	if rec.Code != http.StatusOK {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var l links.Link
	_ = json.Unmarshal(rec.Body.Bytes(), &l)
	if l.ID == "" || l.Source != "instagram" || l.URL != "https://www.instagram.com/reel/A/" || l.Title != "Рецепт" {
		t.Fatalf("created %+v", l)
	}

	p := decodePage(t, linksReq(h, "GET", "/me/links", tok, nil))
	if len(p.Items) != 1 || p.InboxCount != 1 || len(p.Sources) != 1 || p.Sources[0] != "instagram" {
		t.Fatalf("inbox %+v", p)
	}
	if rec := linksReq(h, "PATCH", "/me/links/"+l.ID, tok, map[string]any{"archived": true}); rec.Code != http.StatusNoContent {
		t.Fatalf("archive: %d", rec.Code)
	}
	if p := decodePage(t, linksReq(h, "GET", "/me/links", tok, nil)); len(p.Items) != 0 || p.InboxCount != 0 {
		t.Fatalf("inbox after archive %+v", p)
	}
	if p := decodePage(t, linksReq(h, "GET", "/me/links?archived=1", tok, nil)); len(p.Items) != 1 || !p.Items[0].Archived {
		t.Fatalf("archive %+v", p)
	}
	// Sharing it again brings it back to the inbox under the same id.
	rec = linksReq(h, "POST", "/me/links", tok, map[string]any{"url": "https://www.instagram.com/reel/A/?igsh=2"})
	var again links.Link
	_ = json.Unmarshal(rec.Body.Bytes(), &again)
	if again.ID != l.ID || again.Archived {
		t.Fatalf("re-share %+v", again)
	}
	if rec := linksReq(h, "DELETE", "/me/links/"+l.ID, tok, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", rec.Code)
	}
	if rec := linksReq(h, "DELETE", "/me/links/"+l.ID, tok, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("second delete: %d", rec.Code)
	}
	if rec := linksReq(h, "GET", "/me/links/"+l.ID+"/thumb", tok, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("thumb of deleted: %d", rec.Code)
	}
}

func TestLinksValidation(t *testing.T) {
	h, tok, other, _ := linksServer(t)
	for _, bad := range []string{"javascript:alert(1)", "data:text/html,x", "file:///etc/passwd", "", "https://u:p@x.org/"} {
		if rec := linksReq(h, "POST", "/me/links", tok, map[string]any{"url": bad}); rec.Code != http.StatusBadRequest {
			t.Errorf("url %q: %d, want 400", bad, rec.Code)
		}
	}
	if rec := linksReq(h, "POST", "/me/links", tok, "not an object"); rec.Code != http.StatusBadRequest {
		t.Errorf("bad JSON: %d", rec.Code)
	}
	rec := linksReq(h, "POST", "/me/links", tok, map[string]any{"url": "https://example.org/a"})
	var l links.Link
	_ = json.Unmarshal(rec.Body.Bytes(), &l)
	// Someone else's link does not exist for you.
	if rec := linksReq(h, "PATCH", "/me/links/"+l.ID, other, map[string]any{"archived": true}); rec.Code != http.StatusNotFound {
		t.Errorf("foreign archive: %d", rec.Code)
	}
	if rec := linksReq(h, "DELETE", "/me/links/"+l.ID, other, nil); rec.Code != http.StatusNotFound {
		t.Errorf("foreign delete: %d", rec.Code)
	}
	if p := decodePage(t, linksReq(h, "GET", "/me/links", other, nil)); len(p.Items) != 0 {
		t.Errorf("foreign list leaked %+v", p)
	}
	if rec := linksReq(h, "PATCH", "/me/links/not-a-uuid", tok, map[string]any{"archived": true}); rec.Code != http.StatusNotFound {
		t.Errorf("bad id: %d", rec.Code)
	}
	if rec := linksReq(h, "GET", "/me/links?archived=maybe", tok, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("bad archived flag: %d", rec.Code)
	}
}

func TestLinksRateLimit(t *testing.T) {
	h, tok, other, _ := linksServer(t)
	for i := 0; i < linkCreatesPerMinute; i++ {
		if rec := linksReq(h, "POST", "/me/links", tok, map[string]any{"url": "https://example.org/" + string(rune('a'+i%26)) + "/" + string(rune('0'+i/26))}); rec.Code != http.StatusOK {
			t.Fatalf("create %d: %d", i, rec.Code)
		}
	}
	if rec := linksReq(h, "POST", "/me/links", tok, map[string]any{"url": "https://example.org/over"}); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("over the limit: %d, want 429", rec.Code)
	}
	if rec := linksReq(h, "POST", "/me/links", other, map[string]any{"url": "https://example.org/x"}); rec.Code != http.StatusOK {
		t.Fatalf("the limit is per user: %d", rec.Code)
	}
}

func TestLinksThumbHeaders(t *testing.T) {
	h, tok, _, q, s := linksServerWith(t)
	rec := linksReq(h, "POST", "/me/links", tok, map[string]any{"url": "https://example.org/p"})
	var l links.Link
	_ = json.Unmarshal(rec.Body.Bytes(), &l)
	if rec := linksReq(h, "GET", "/me/links/"+l.ID+"/thumb", tok, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("no thumb yet: %d", rec.Code)
	}
	s.links.Wait() // the background fetch must not overwrite the thumbnail set below
	id, _ := db.ParseUUID(l.ID)
	if err := q.SetSavedLinkEnriched(context.Background(), db.SetSavedLinkEnrichedParams{ID: id, Thumb: []byte{0xFF, 0xD8, 0xFF}}); err != nil {
		t.Fatal(err)
	}
	rec = linksReq(h, "GET", "/me/links/"+l.ID+"/thumb", tok, nil)
	hd := rec.Header()
	if rec.Code != http.StatusOK || hd.Get("Content-Type") != "image/jpeg" || hd.Get("X-Content-Type-Options") != "nosniff" ||
		hd.Get("Content-Security-Policy") != "default-src 'none'; sandbox" || hd.Get("Cache-Control") != "private, max-age=86400" {
		t.Fatalf("thumb: %d %v", rec.Code, hd)
	}
}

func TestLinksSharedAtKeepsArchive(t *testing.T) {
	h, tok, _, _ := linksServer(t)
	rec := linksReq(h, "POST", "/me/links", tok, map[string]any{"url": "https://example.org/q"})
	var l links.Link
	_ = json.Unmarshal(rec.Body.Bytes(), &l)
	linksReq(h, "PATCH", "/me/links/"+l.ID, tok, map[string]any{"archived": true})
	old := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	rec = linksReq(h, "POST", "/me/links", tok, map[string]any{"url": "https://example.org/q", "shared_at": old})
	var again links.Link
	_ = json.Unmarshal(rec.Body.Bytes(), &again)
	if rec.Code != http.StatusOK || !again.Archived {
		t.Fatalf("an old queued share un-archived the link: %d %+v", rec.Code, again)
	}
	if rec := linksReq(h, "POST", "/me/links", tok, map[string]any{"url": "https://example.org/q", "shared_at": "yesterday"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad shared_at: %d", rec.Code)
	}
}
