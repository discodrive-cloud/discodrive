package subsonic

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/db"
)

// createBookmark refuses ids the caller cannot play: another user's unshared
// song, or an id that does not exist at all.
func TestCreateBookmarkRequiresAccess(t *testing.T) {
	h, ctx, userAID, _ := setupTwo(t)

	nodeID := makeNode(t, ctx, h.q, userAID, pgtype.UUID{}, "song.mp3", false)
	_, _, song := seed(t, ctx, h.q, userAID, nodeID, "Artist", "Album", "Private Song")
	trID := encID("tr", db.UUIDString(song.ID))

	if resp := doGet(h, "apikeyB", "createBookmark", "id="+trID+"&position=1"); resp["status"] == "ok" {
		t.Errorf("B bookmarked A's unshared song")
	}
	if resp := doGet(h, "apikeyB", "createBookmark", "id=pe-00000000-0000-0000-0000-000000000001&position=1"); resp["status"] == "ok" {
		t.Errorf("B bookmarked a nonexistent episode")
	}
	if resp := doGet(h, "apikeyB", "getBookmarks", ""); len(bookmarkList(t, resp)) != 0 {
		t.Errorf("B has bookmarks: %v", resp)
	}
	if resp := doGet(h, testAPIKey, "createBookmark", "id="+trID+"&position=1"); resp["status"] != "ok" {
		t.Errorf("owner bookmark refused: %v", resp)
	}
}
