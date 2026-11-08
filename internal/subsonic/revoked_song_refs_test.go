package subsonic

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/db"
)

// A playlist, the play queue and now-playing keep song ids, not access: once a share
// is revoked, the owner's songs must drop out of them the way getSong and getBookmarks
// already refuse them.
func TestRevokedShareLeavesNoSongMetadata(t *testing.T) {
	h, ctx, userA, userB := setupTwo(t)
	folder := makeNode(t, ctx, h.q, userA, pgtype.UUID{}, "music", true)
	node := makeNode(t, ctx, h.q, userA, folder, "s.mp3", false)
	_, _, song := seed(t, ctx, h.q, userA, node, "Owner-Artist", "Owner-Album", "Secret-Title")
	share, err := h.q.CreateShare(ctx, db.CreateShareParams{
		ResourceType: "file_node", ResourceID: folder, OwnerID: userA, SharedWithUser: userB, Access: "read",
	})
	if err != nil {
		t.Fatal(err)
	}
	tr := encID("tr", db.UUIDString(song.ID))
	for _, call := range []struct{ method, query string }{
		{"createPlaylist", "name=p&songId=" + tr},
		{"savePlayQueue", "id=" + tr},
		{"scrobble", "id=" + tr},
	} {
		if resp := doGet(h, "apikeyB", call.method, call.query); resp["status"] != "ok" {
			t.Fatalf("%s while shared: %v", call.method, resp)
		}
	}
	if err := h.q.DeleteShare(ctx, share.ID); err != nil {
		t.Fatal(err)
	}

	lists := doGet(h, "apikeyB", "getPlaylists", "")
	raw, _ := json.Marshal(lists)
	if strings.Contains(string(raw), `"songCount":1`) {
		t.Errorf("getPlaylists still counts the revoked song: %s", raw)
	}
	var plID string
	if pl, ok := lists["playlists"].(map[string]any)["playlist"].([]any); ok && len(pl) > 0 {
		plID, _ = pl[0].(map[string]any)["id"].(string)
	}
	for _, call := range []struct{ method, query string }{
		{"getPlaylist", "id=" + plID},
		{"getPlayQueue", ""},
		{"getNowPlaying", ""},
	} {
		resp := doGet(h, "apikeyB", call.method, call.query)
		b, _ := json.Marshal(resp)
		if strings.Contains(string(b), "Secret-Title") || strings.Contains(string(b), "Owner-Album") {
			t.Errorf("%s still shows the revoked song: %s", call.method, b)
		}
	}
}
