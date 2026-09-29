package api

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	"discodrive/internal/db"
	"discodrive/internal/saved"
	"discodrive/internal/storage"
)

func pngBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{255, 0, 0, 255})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// The Pocket reader's CSP (img-src 'self' data: blob:) blocks external article
// images. The reader now loads them through GET /me/saved/{id}/image as blobs: raster
// only (sniffed, nosniff), capped, SSRF-guarded, nothing of the user forwarded.
func TestSavedImageProxy(t *testing.T) {
	h := newBookmarkHarness(t)
	ctx := context.Background()
	pic := pngBytes(t)
	var mu sync.Mutex
	var seen []http.Header
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Clone())
		mu.Unlock()
		switch r.URL.Path {
		case "/ok.png":
			w.Header().Set("Content-Type", "application/octet-stream") // wrong on purpose: we sniff
			_, _ = w.Write(pic)
		case "/fake.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("<html><script>alert(1)</script></html>"))
		case "/pic.svg":
			w.Header().Set("Content-Type", "image/svg+xml")
			_, _ = w.Write([]byte(`<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"/>`))
		case "/big.png":
			w.Header().Set("Content-Length", strconv.Itoa(saved.MaxImageBytes+1))
			_, _ = w.Write(pic)
		default:
			http.NotFound(w, r)
		}
	}))
	defer up.Close()

	svc := saved.NewService(h.q, storage.NewLocalDisk(t.TempDir()), 0)
	svc.Client = &http.Client{}
	svc.Validate = func(raw string) error {
		if strings.Contains(raw, "blocked") {
			return errors.New("fetchguard: blocked URL: nas.internal -> 100.101.102.103")
		}
		return nil
	}
	h.s.saved = svc
	handler := h.svc.Middleware(http.HandlerFunc(h.s.handleSavedImage))

	article, err := h.q.UpsertSavedItem(ctx, db.UpsertSavedItemParams{UserID: h.uid, Url: "https://news.example/a", Kind: saved.KindArticle})
	if err != nil {
		t.Fatal(err)
	}
	download, err := h.q.UpsertSavedItem(ctx, db.UpsertSavedItemParams{UserID: h.uid, Url: "https://news.example/f.zip", Kind: saved.KindDownload})
	if err != nil {
		t.Fatal(err)
	}
	get := func(itemID, imgURL, tok string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/me/saved/"+itemID+"/image?url="+url.QueryEscape(imgURL), nil)
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		req.Header.Set("Cookie", "kf_secret=1")
		req.SetPathValue("id", itemID)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	aid := db.UUIDString(article.ID)

	rec := get(aid, up.URL+"/ok.png", h.tok)
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), pic) {
		t.Fatalf("png: %d, %d bytes", rec.Code, rec.Body.Len())
	}
	if rec.Header().Get("Content-Type") != "image/png" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("png headers: %v", rec.Header())
	}
	for _, hd := range seen {
		if hd.Get("Cookie") != "" || hd.Get("Authorization") != "" || hd.Get("Referer") != "" {
			t.Fatalf("user credentials reached the image host: %v", hd)
		}
	}

	for name, c := range map[string]struct {
		item, url, tok string
		want           int
	}{
		"html posing as png": {aid, up.URL + "/fake.png", h.tok, http.StatusUnsupportedMediaType},
		"svg":                {aid, up.URL + "/pic.svg", h.tok, http.StatusUnsupportedMediaType},
		"too large":          {aid, up.URL + "/big.png", h.tok, http.StatusRequestEntityTooLarge},
		"upstream 404":       {aid, up.URL + "/missing.png", h.tok, http.StatusBadGateway},
		"ssrf blocked":       {aid, "http://blocked.internal/x.png", h.tok, http.StatusBadGateway},
		"javascript url":     {aid, "javascript:alert(1)", h.tok, http.StatusBadRequest},
		"download item":      {db.UUIDString(download.ID), up.URL + "/ok.png", h.tok, http.StatusNotFound},
		"no auth":            {aid, up.URL + "/ok.png", "", http.StatusUnauthorized},
	} {
		rec := get(c.item, c.url, c.tok)
		if rec.Code != c.want {
			t.Fatalf("%s: %d %s, want %d", name, rec.Code, rec.Body, c.want)
		}
		if strings.Contains(rec.Body.String(), "100.101") || strings.Contains(rec.Body.String(), "<script") {
			t.Fatalf("%s: body leaks upstream data: %s", name, rec.Body)
		}
	}

	// Another user cannot use someone else's article as a fetch handle.
	otherTok, _, err := h.svc.Register(ctx, "img-other@x.test", "password12")
	if err != nil {
		t.Fatal(err)
	}
	if rec := get(aid, up.URL+"/ok.png", otherTok); rec.Code != http.StatusNotFound {
		t.Fatalf("foreign article: %d, want 404", rec.Code)
	}
}
