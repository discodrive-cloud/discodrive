package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"discodrive/internal/db"
)

// An upload session opened by path must report InitByPath's refusal. The error used to
// be shadowed inside the path branch, so the client got 201 with an empty upload_id.
func TestUploadInitByPathReportsInitError(t *testing.T) {
	c := chunkServer(t)
	rec, out := c.json("POST", "/upload/init", map[string]any{"path": "docs/a.txt", "size": -1})
	if rec.Code == http.StatusCreated {
		t.Fatalf("init with a negative size: 201 %v, want an error status", out)
	}
	if id, _ := out["upload_id"].(string); id != "" {
		t.Fatalf("got upload_id %q with an error", id)
	}
}

func TestRegisterClosedUntilEnabled(t *testing.T) {
	_, q, svc := bootstrapPairingDB(t)
	s := &Server{auth: svc, q: q}
	body := map[string]any{"email": "self@x.test", "password": "password12"}

	rec, _ := doPost(http.HandlerFunc(s.handleRegister), "/auth/register", "", body)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("register on a closed instance: %d, want 404", rec.Code)
	}
	if _, err := q.GetUserByEmail(context.Background(), "self@x.test"); err == nil {
		t.Fatal("closed registration still created the account")
	}

	if err := q.UpsertSetting(context.Background(), db.UpsertSettingParams{Key: registrationSettingKey, Value: "true"}); err != nil {
		t.Fatalf("enable: %v", err)
	}
	rec, m := doPost(http.HandlerFunc(s.handleRegister), "/auth/register", "", body)
	if rec.Code != http.StatusCreated || m["token"] == nil {
		t.Fatalf("register with registration.enabled: %d %v", rec.Code, m)
	}
}

// A bulk import stamps all its rows with one seq. The changes feed pages by seq, so a
// page must not end inside a seq group: the rest of the group would be skipped.
func TestBookmarkChangesNeverSplitSeqGroup(t *testing.T) {
	h := newBookmarkHarness(t)
	bulk := func(n int) map[string]bool {
		t.Helper()
		ids := map[string]bool{}
		items := make([]map[string]any, 0, n)
		for range n {
			id := uuid.NewString()
			ids[id] = true
			items = append(items, map[string]any{"id": id, "title": "b", "url": "https://example.com/" + id})
		}
		if rec, m := h.do(t, http.MethodPost, "/me/bookmarks/bulk", map[string]any{"items": items}, ""); rec.Code != http.StatusOK {
			t.Fatalf("bulk %d: %d %v", n, rec.Code, m)
		}
		return ids
	}
	// Two groups that straddle the 1000-row page, then one group larger than a page.
	want := map[string]bool{}
	for _, n := range []int{600, 600, changesPageLimit + 500} {
		for id := range bulk(n) {
			want[id] = true
		}
	}

	got := map[string]bool{}
	since := "0"
	for pages := 0; ; pages++ {
		if pages > 10 {
			t.Fatal("changes feed does not terminate")
		}
		m, items := h.changes(t, since)
		for _, it := range items {
			got[it["id"].(string)] = true
		}
		since = formatFloat(m["cursor"].(float64))
		if !m["has_more"].(bool) {
			break
		}
	}
	if len(got) != len(want) {
		t.Fatalf("changes feed delivered %d of %d bookmarks", len(got), len(want))
	}
	for id := range want {
		if !got[id] {
			t.Fatalf("bookmark %s never delivered", id)
		}
	}
}
