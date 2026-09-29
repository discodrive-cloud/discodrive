package subsonic

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/db"
)

// An album whose songs are in a shared folder may take its cover from a folder
// that is not shared. The sharee sees the album but must not get that file.
func TestGetCoverArtChecksAccessToTheCoverNode(t *testing.T) {
	h, ctx, userA, userB := setupTwo(t)
	h.storageRoot = t.TempDir()
	h.xaccel = false

	shared := makeNode(t, ctx, h.q, userB, pgtype.UUID{}, "shared", true)
	private := makeNode(t, ctx, h.q, userB, pgtype.UUID{}, "private", true)
	songNode := makeNode(t, ctx, h.q, userB, shared, "s.mp3", false)
	_, album, _ := seed(t, ctx, h.q, userB, songNode, "Artist", "Album", "Song")

	coverRel := "b/private/cover.jpg"
	if err := os.MkdirAll(filepath.Join(h.storageRoot, "b/private"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.storageRoot, coverRel), []byte("\xff\xd8\xffPRIVATE"), 0o644); err != nil {
		t.Fatal(err)
	}
	cover, err := h.q.CreateNode(ctx, db.CreateNodeParams{
		UserID: userB, ParentID: private, Name: "cover.jpg",
		DiskPath: pgtype.Text{String: coverRel, Valid: true},
		Mime:     pgtype.Text{String: "image/jpeg", Valid: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.q.SetAlbumCover(ctx, db.SetAlbumCoverParams{ID: album.ID, CoverArt: pgtype.Text{String: db.UUIDString(cover.ID), Valid: true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.q.CreateShare(ctx, db.CreateShareParams{
		ResourceType: "file_node", ResourceID: shared, OwnerID: userB, SharedWithUser: userA, Access: "read",
	}); err != nil {
		t.Fatal(err)
	}

	get := func(apiKey string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
			"/rest/getCoverArt?id="+encID("al", db.UUIDString(album.ID))+"&apiKey="+apiKey+"&f=json&c=test&v=1.16.1", nil))
		return rec
	}
	if rec := get(testAPIKey); rec.Code == http.StatusOK {
		t.Fatalf("sharee got the cover from an unshared folder: %q", rec.Body.String())
	}
	if rec := get("apikeyB"); rec.Code != http.StatusOK {
		t.Fatalf("owner: %d %s", rec.Code, rec.Body.String())
	}
}
