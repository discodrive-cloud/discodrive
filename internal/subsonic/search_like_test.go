package subsonic

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

// LIKE wildcards in the query match literally: "%" finds "100% Pure", not the
// whole library, and "_" finds "a_b", not "axb".
func TestSearch3WildcardsAreLiteral(t *testing.T) {
	h, ctx := setupSubsonic(t)
	userUUID := mustUserID(t, ctx, h.q, testEmail)
	n1 := makeNode(t, ctx, h.q, userUUID, pgtype.UUID{}, "p.mp3", false)
	n2 := makeNode(t, ctx, h.q, userUUID, pgtype.UUID{}, "q.mp3", false)
	seed(t, ctx, h.q, userUUID, n1, "100% Pure", "Album a_b", "Song 50%")
	seed(t, ctx, h.q, userUUID, n2, "Plain Artist", "Album axb", "Other Song")

	resp := doGet(h, testAPIKey, "search3", "query=%25")
	if got := collectSearchArtists(resp, "searchResult3"); len(got) != 1 || got[0] != "100% Pure" {
		t.Errorf("artists for %%: %v, want only 100%% Pure", got)
	}
	if got := collectSearchSongs(resp, "searchResult3"); len(got) != 1 || got[0] != "Song 50%" {
		t.Errorf("songs for %%: %v, want only Song 50%%", got)
	}
	resp = doGet(h, testAPIKey, "search3", "query=a_b")
	if got := collectSearchAlbums(resp, "searchResult3"); len(got) != 1 || got[0] != "Album a_b" {
		t.Errorf("albums for a_b: %v, want only Album a_b", got)
	}
}
