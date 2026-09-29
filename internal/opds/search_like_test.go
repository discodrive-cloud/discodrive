package opds

import (
	"encoding/xml"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/db"
)

// "%" and "_" in the search box match literally instead of acting as wildcards.
func TestSearchWildcardsAreLiteral(t *testing.T) {
	h, _ := setupOPDS(t)
	seedAcqBooks(t, h)
	ctx := t.Context()
	user, err := h.q.GetUserByEmail(ctx, testEmail)
	if err != nil {
		t.Fatal(err)
	}
	n, err := h.q.CreateNode(ctx, db.CreateNodeParams{UserID: user.ID, Name: "pct", IsDir: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.q.UpsertBook(ctx, db.UpsertBookParams{
		UserID: user.ID, NodeID: n.ID, Title: "100% Proof", SortTitle: "100% proof",
		Format: "epub", ContentType: "application/epub+zip", Size: pgtype.Int8{Int64: 1, Valid: true},
	}); err != nil {
		t.Fatal(err)
	}

	for q, want := range map[string]string{"%25": "100% Proof", "0_": ""} {
		rec := opdsGetBasic(h, "/opds/search?q="+q, testEmail, testPassword)
		if rec.Code != http.StatusOK {
			t.Fatalf("q=%s: %d", q, rec.Code)
		}
		var feed atomFeed
		if err := xml.Unmarshal(rec.Body.Bytes(), &feed); err != nil {
			t.Fatal(err)
		}
		switch {
		case want == "" && len(feed.Entries) != 0:
			t.Errorf("q=%s matched %d books, want none", q, len(feed.Entries))
		case want != "" && (len(feed.Entries) != 1 || feed.Entries[0].Title != want):
			t.Errorf("q=%s matched %d books, want only %q", q, len(feed.Entries), want)
		}
	}
}
