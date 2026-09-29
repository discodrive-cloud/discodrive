package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"discodrive/internal/db"
	"discodrive/internal/saved"
	"discodrive/internal/storage"
)

func bulkItem(id, parent string, folder bool, url string) map[string]any {
	m := map[string]any{"id": id, "is_folder": folder, "title": id[:4], "url": url}
	if parent != "" {
		m["parent_id"] = parent
	}
	return m
}

func (h *bookmarkHarness) bulk(t *testing.T, items ...map[string]any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	return h.do(t, http.MethodPost, "/me/bookmarks/bulk", map[string]any{"items": items}, "")
}

func (h *bookmarkHarness) live(t *testing.T) map[string]bool {
	t.Helper()
	rows, err := h.q.ListBrowserBookmarks(context.Background(), h.uid)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, r := range rows {
		out[db.UUIDString(r.ID)] = true
	}
	return out
}

// Bulk import took any parent_id: itself, a cycle, a bookmark, somebody else's
// node. A cycle made its nodes undeletable and invisible and hung the recursive
// tree queries; now the whole import is refused and nothing is written.
func TestBookmarkBulkImportValidatesParents(t *testing.T) {
	h := newBookmarkHarness(t)
	ctx := context.Background()
	id := func() string { return uuid.NewString() }

	// A tree already on the server: top ← mid (folders), leaf (bookmark) in mid.
	top, mid, leaf := id(), id(), id()
	if rec, _ := h.bulk(t, bulkItem(top, "", true, ""), bulkItem(mid, top, true, ""), bulkItem(leaf, mid, false, "https://a.example/")); rec.Code != http.StatusOK {
		t.Fatalf("seed import: %d %s", rec.Code, rec.Body)
	}
	// Another user's folder and bookmark.
	otherTok, _, err := h.svc.Register(ctx, "other-bm@x.test", "password12")
	if err != nil {
		t.Fatal(err)
	}
	foreignFolder := id()
	other := &bookmarkHarness{s: h.s, svc: h.svc, q: h.q, tok: otherTok}
	if rec, _ := other.bulk(t, bulkItem(foreignFolder, "", true, "")); rec.Code != http.StatusOK {
		t.Fatalf("other user's import: %d", rec.Code)
	}

	a, b := id(), id()
	bad := map[string][]map[string]any{
		"own parent":            {bulkItem(a, a, true, "")},
		"cycle inside import":   {bulkItem(a, b, true, ""), bulkItem(b, a, true, "")},
		"bookmark in import":    {bulkItem(a, "", false, "https://x.example/"), bulkItem(b, a, false, "https://y.example/")},
		"stored bookmark":       {bulkItem(a, leaf, false, "https://x.example/")},
		"unknown parent":        {bulkItem(a, id(), false, "https://x.example/")},
		"another user's folder": {bulkItem(a, foreignFolder, false, "https://x.example/")},
		"another user's id":     {bulkItem(foreignFolder, "", true, "")},
		"cycle through stored":  {bulkItem(top, mid, true, "")}, // mid is top's child
		"duplicate id":          {bulkItem(a, "", true, ""), bulkItem(a, "", true, "")},
	}
	for name, items := range bad {
		before := h.live(t)
		if rec, _ := h.bulk(t, items...); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s, want 400", name, rec.Code, rec.Body)
		}
		if after := h.live(t); len(after) != len(before) {
			t.Fatalf("%s: a refused import wrote %d rows", name, len(after)-len(before))
		}
	}
	// The stored tree is intact and still deletable.
	if rec, _ := h.do(t, http.MethodDelete, "/me/bookmarks/"+top, nil, top); rec.Code != http.StatusNoContent {
		t.Fatalf("delete top after refused imports: %d", rec.Code)
	}

	// Legit: a parent from an earlier chunk, and a tombstoned folder the import revives.
	c1, c2 := id(), id()
	if rec, _ := h.bulk(t, bulkItem(c1, "", true, "")); rec.Code != http.StatusOK {
		t.Fatal("chunk 1")
	}
	if rec, b := h.bulk(t, bulkItem(c2, c1, false, "https://c.example/"), bulkItem(id(), mid, false, "https://d.example/")); rec.Code != http.StatusOK {
		t.Fatalf("chunk 2 under a stored and a tombstoned folder: %d %v", rec.Code, b)
	}
}

// A cycle already in the table (written by the old import) must not hang delete or
// move: the recursive walks stop at nodes they have already seen.
func TestBookmarkTreeQueriesSurviveStoredCycle(t *testing.T) {
	h := newBookmarkHarness(t)
	ctx := context.Background()
	a, b, c := uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, row := range [][2]string{{a, b}, {b, a}, {c, ""}} {
		var parent any
		if row[1] != "" {
			parent = row[1]
		}
		if _, err := h.pool.Exec(ctx, `INSERT INTO browser_bookmarks (id, user_id, parent_id, is_folder, title, seq) VALUES ($1, $2, $3, true, 'x', 1)`, row[0], h.uid, parent); err != nil {
			t.Fatal(err)
		}
	}
	// The request context times out, so on a hang Postgres cancels the runaway query.
	run := func(method, path, body, pathID string) int {
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		var handler http.HandlerFunc = h.s.handleBookmarkDelete
		if method == http.MethodPatch {
			handler = h.s.handleBookmarkUpdate
		}
		req := httptest.NewRequestWithContext(cctx, method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+h.tok)
		req.SetPathValue("id", pathID)
		rec := httptest.NewRecorder()
		h.svc.Middleware(handler).ServeHTTP(rec, req)
		return rec.Code
	}
	done := make(chan [2]int, 1)
	go func() {
		move := run(http.MethodPatch, "/me/bookmarks/"+c, `{"parent_id":"`+a+`"}`, c)
		del := run(http.MethodDelete, "/me/bookmarks/"+a, "", a)
		done <- [2]int{move, del}
	}()
	select {
	case codes := <-done:
		if codes[0] != http.StatusOK || codes[1] != http.StatusNoContent {
			t.Fatalf("move=%d delete=%d on a cyclic tree, want 200/204", codes[0], codes[1])
		}
	case <-time.After(20 * time.Second):
		t.Fatal("tree queries hung on a stored cycle")
	}
}

// javascript:/data: URLs are rendered as clickable links (search, Pocket): refuse
// them on create and edit; in a bulk import leave them out instead of failing the
// whole initial sync. Rows stored earlier stay readable.
func TestBookmarkAndSavedRejectNonWebURLs(t *testing.T) {
	h := newBookmarkHarness(t)
	ctx := context.Background()
	for _, u := range []string{"javascript:alert(1)", "JavaScript:alert(1)", "data:text/html,<script>x</script>", "file:///etc/passwd", "example.com", "https://"} {
		if rec, _ := h.do(t, http.MethodPost, "/me/bookmarks", map[string]any{"title": "x", "url": u}, ""); rec.Code != http.StatusBadRequest {
			t.Fatalf("create %q: %d, want 400", u, rec.Code)
		}
	}
	rec, m := h.do(t, http.MethodPost, "/me/bookmarks", map[string]any{"title": "ok", "url": "https://ok.example/"}, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("create web link: %d", rec.Code)
	}
	okID := m["id"].(string)
	if rec, _ := h.do(t, http.MethodPatch, "/me/bookmarks/"+okID, map[string]any{"url": "javascript:alert(1)"}, okID); rec.Code != http.StatusBadRequest {
		t.Fatalf("patch to javascript: %d, want 400", rec.Code)
	}

	js, web := uuid.NewString(), uuid.NewString()
	rec, out := h.bulk(t, bulkItem(js, "", false, "javascript:void(0)"), bulkItem(web, "", false, "http://w.example/"))
	if rec.Code != http.StatusOK || out["skipped"] != float64(1) || out["count"] != float64(1) {
		t.Fatalf("bulk with a bookmarklet: %d %v, want 200 with 1 imported, 1 skipped", rec.Code, out)
	}
	if live := h.live(t); live[js] || !live[web] {
		t.Fatalf("bulk stored the bookmarklet or dropped the web link: %v", live)
	}

	// A legacy row stays listed.
	legacy := uuid.NewString()
	if _, err := h.pool.Exec(ctx, `INSERT INTO browser_bookmarks (id, user_id, title, url, seq) VALUES ($1, $2, 'old', 'javascript:1', 1)`, legacy, h.uid); err != nil {
		t.Fatal(err)
	}
	if !h.live(t)[legacy] {
		t.Fatal("a stored non-web bookmark disappeared from the list")
	}

	// Saved: same rule, also with client-supplied content (no server fetch).
	savedSvc := saved.NewService(h.q, storage.NewLocalDisk(t.TempDir()), 0)
	savedSvc.Validate = func(string) error { return nil }
	h.s.saved = savedSvc
	create := h.svc.Middleware(http.HandlerFunc(h.s.handleSavedCreate))
	for _, u := range []string{"javascript:alert(1)", "data:text/html,x"} {
		if rec, _ := doPost(create, "/me/saved", h.tok, map[string]any{"url": u, "kind": "article", "content_html": "<p>x</p>"}); rec.Code != http.StatusBadRequest {
			t.Fatalf("saved %q: %d, want 400", u, rec.Code)
		}
	}
}

// The SSRF guard's error names the addresses a host resolved to; the 400 used to
// echo it, leaking the internal DNS view to any user.
func TestSavedCreateDoesNotLeakResolution(t *testing.T) {
	h := newBookmarkHarness(t)
	savedSvc := saved.NewService(h.q, storage.NewLocalDisk(t.TempDir()), 0)
	savedSvc.Validate = func(string) error {
		return errors.New("fetchguard: blocked URL: nas.internal -> 100.101.102.103")
	}
	h.s.saved = savedSvc
	create := h.svc.Middleware(http.HandlerFunc(h.s.handleSavedCreate))
	rec, _ := doPost(create, "/me/saved", h.tok, map[string]any{"url": "http://nas.internal/x", "kind": "download"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
	if body := rec.Body.String(); strings.Contains(body, "100.101") || strings.Contains(body, "nas.internal") {
		t.Fatalf("400 leaks the resolution: %s", body)
	}
}
