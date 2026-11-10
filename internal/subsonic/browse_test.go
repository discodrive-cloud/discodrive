package subsonic

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/db"
	"discodrive/internal/secret"
)

// --- ID round-trip ---

func TestIDRoundTrip(t *testing.T) {
	cases := []struct{ kind, uuid string }{
		{"ar", "550e8400-e29b-41d4-a716-446655440000"},
		{"al", "00000000-0000-0000-0000-000000000001"},
		{"tr", "aaaabbbb-cccc-dddd-eeee-ffff00001111"},
		{"pl", "12345678-1234-1234-1234-123456789abc"},
	}
	for _, tc := range cases {
		enc := encID(tc.kind, tc.uuid)
		kind, uuid, ok := decID(enc)
		if !ok {
			t.Errorf("decID(%q) ok=false", enc)
			continue
		}
		if kind != tc.kind {
			t.Errorf("kind: got %q, want %q", kind, tc.kind)
		}
		if uuid != tc.uuid {
			t.Errorf("uuid: got %q, want %q", uuid, tc.uuid)
		}
	}

	// Malformed inputs should all return ok=false.
	bad := []string{"", "noprefix", "-", "-uuid", "ar-"}
	for _, s := range bad {
		if _, _, ok := decID(s); ok {
			t.Errorf("decID(%q) should be malformed but got ok=true", s)
		}
	}
}

// --- Helpers ---

// setupTwo spins up the standard test container (via setupSubsonic which creates user A),
// then adds user B with their own credentials. Returns handler, ctx, userA ID, userB ID.
func setupTwo(t *testing.T) (*Handler, context.Context, pgtype.UUID, pgtype.UUID) {
	t.Helper()
	ctx := context.Background()

	h, _ := setupSubsonic(t) // creates user A (testEmail / testAPIKey)

	uA, err := h.q.GetUserByEmail(ctx, testEmail)
	if err != nil {
		t.Fatalf("GetUserByEmail(A): %v", err)
	}

	uB, err := h.q.CreateUser(ctx, db.CreateUserParams{
		TenantID:     uA.TenantID,
		Email:        "b@x.test",
		PasswordHash: "irrelevant",
		Role:         "user",
	})
	if err != nil {
		t.Fatalf("CreateUser(B): %v", err)
	}

	cipher, err := secret.New(testKey)
	if err != nil {
		t.Fatalf("secret.New: %v", err)
	}
	ct, err := cipher.Encrypt("passB")
	if err != nil {
		t.Fatalf("cipher.Encrypt(B): %v", err)
	}

	if _, err = h.q.UpsertMusicSettings(ctx, db.UpsertMusicSettingsParams{
		UserID:  uB.ID,
		Enabled: true,
	}); err != nil {
		t.Fatalf("UpsertMusicSettings(B): %v", err)
	}
	if err = h.q.SetMusicCredentials(ctx, db.SetMusicCredentialsParams{
		UserID:         uB.ID,
		PasswordCipher: pgtype.Text{String: ct, Valid: true},
		ApiKey:         pgtype.Text{String: "apikeyB", Valid: true},
	}); err != nil {
		t.Fatalf("SetMusicCredentials(B): %v", err)
	}

	return h, ctx, uA.ID, uB.ID
}

// makeNode creates a node and returns its ID.
func makeNode(t *testing.T, ctx context.Context, q *db.Queries, ownerID, parentID pgtype.UUID, name string, isDir bool) pgtype.UUID {
	t.Helper()
	n, err := q.CreateNode(ctx, db.CreateNodeParams{
		UserID:   ownerID,
		ParentID: parentID,
		Name:     name,
		IsDir:    isDir,
	})
	if err != nil {
		t.Fatalf("CreateNode(%s): %v", name, err)
	}
	return n.ID
}

// seed inserts an artist, album, and one song owned by userID with the given nodeID.
func seed(t *testing.T, ctx context.Context, q *db.Queries,
	userID, nodeID pgtype.UUID, artistName, albumName, songTitle string,
) (db.Artist, db.Album, db.Song) {
	t.Helper()

	artist, err := q.UpsertArtist(ctx, db.UpsertArtistParams{
		UserID:   userID,
		Name:     artistName,
		SortName: artistName,
	})
	if err != nil {
		t.Fatalf("UpsertArtist(%q): %v", artistName, err)
	}

	album, err := q.UpsertAlbum(ctx, db.UpsertAlbumParams{
		UserID:   userID,
		ArtistID: artist.ID,
		Name:     albumName,
		Year:     pgtype.Int4{Int32: 2024, Valid: true},
	})
	if err != nil {
		t.Fatalf("UpsertAlbum(%q): %v", albumName, err)
	}

	song, err := q.UpsertSong(ctx, db.UpsertSongParams{
		UserID:   userID,
		AlbumID:  album.ID,
		ArtistID: artist.ID,
		NodeID:   nodeID,
		Title:    songTitle,
		Duration: pgtype.Int4{Int32: 180, Valid: true},
		Track:    pgtype.Int4{Int32: 1, Valid: true},
		Suffix:   pgtype.Text{String: "mp3", Valid: true},
	})
	if err != nil {
		t.Fatalf("UpsertSong(%q): %v", songTitle, err)
	}
	if err := q.RefreshAlbumSongCount(ctx, album.ID); err != nil {
		t.Fatalf("RefreshAlbumSongCount: %v", err)
	}
	return artist, album, song
}

// doGet sends a JSON Subsonic request authenticated via apiKey and returns the
// parsed inner subsonic-response map.
func doGet(h *Handler, apiKey, endpoint, queryExtra string) map[string]any {
	target := fmt.Sprintf("/rest/%s?apiKey=%s&f=json&c=test&v=1.16.1", endpoint, apiKey)
	if queryExtra != "" {
		target += "&" + queryExtra
	}
	_, m := subsonicGet(h, target)
	return subsonicResponse(m)
}

// artistNames collects every artist name from a getArtists/getIndexes response map.
func artistNames(resp map[string]any, key string) map[string]bool {
	names := map[string]bool{}
	wrapper, _ := resp[key].(map[string]any)
	if wrapper == nil {
		return names
	}
	index, _ := wrapper["index"].([]any)
	for _, group := range index {
		g, _ := group.(map[string]any)
		for _, a := range g["artist"].([]any) {
			ao, _ := a.(map[string]any)
			if n, ok := ao["name"].(string); ok {
				names[n] = true
			}
		}
	}
	return names
}

// --- Tests ---

func TestGetArtistsListsOwn(t *testing.T) {
	h, ctx, userAID, _ := setupTwo(t)

	// Create stub file nodes owned by A (CreateNode returns the actual ID).
	nA1 := makeNode(t, ctx, h.q, userAID, pgtype.UUID{}, "a1.mp3", false)
	nA2 := makeNode(t, ctx, h.q, userAID, pgtype.UUID{}, "a2.mp3", false)

	seed(t, ctx, h.q, userAID, nA1, "Arctic Monkeys", "AM", "Do I Wanna Know?")
	seed(t, ctx, h.q, userAID, nA2, "Blur", "Parklife", "Girls & Boys")

	resp := doGet(h, testAPIKey, "getArtists", "")
	if resp == nil || resp["status"] != "ok" {
		t.Fatalf("getArtists failed: %v", resp)
	}

	names := artistNames(resp, "artists")
	for _, want := range []string{"Arctic Monkeys", "Blur"} {
		if !names[want] {
			t.Errorf("expected artist %q, got: %v", want, names)
		}
	}
}

func TestGetAlbumReturnsSongs(t *testing.T) {
	h, ctx, userAID, _ := setupTwo(t)

	nodeID := makeNode(t, ctx, h.q, userAID, pgtype.UUID{}, "paranoid.mp3", false)
	_, album, _ := seed(t, ctx, h.q, userAID, nodeID, "Radiohead", "OK Computer", "Paranoid Android")

	resp := doGet(h, testAPIKey, "getAlbum", "id="+encID("al", db.UUIDString(album.ID)))
	if resp == nil || resp["status"] != "ok" {
		t.Fatalf("getAlbum failed: %v", resp)
	}

	albumObj, ok := resp["album"].(map[string]any)
	if !ok {
		t.Fatalf("no album in response: %v", resp)
	}
	if albumObj["name"] != "OK Computer" {
		t.Errorf("album.name = %v, want OK Computer", albumObj["name"])
	}

	songs, ok := albumObj["song"].([]any)
	if !ok || len(songs) == 0 {
		t.Fatalf("no songs in album response: %v", albumObj)
	}

	song := songs[0].(map[string]any)
	if song["title"] != "Paranoid Android" {
		t.Errorf("song.title = %v, want Paranoid Android", song["title"])
	}

	// Verify song id has the "tr" prefix.
	songID, _ := song["id"].(string)
	kind, _, ok2 := decID(songID)
	if !ok2 || kind != "tr" {
		t.Errorf("song id %q should decode to kind=tr", songID)
	}
}

func TestScopingOwnPlusShared(t *testing.T) {
	h, ctx, userAID, userBID := setupTwo(t)

	// User A has their own song.
	nA := makeNode(t, ctx, h.q, userAID, pgtype.UUID{}, "a-song.mp3", false)
	seed(t, ctx, h.q, userAID, nA, "A-Artist", "A-Album", "A-Song")

	// User B has two folders: one will be shared with A, one will stay private.
	sharedFolder := makeNode(t, ctx, h.q, userBID, pgtype.UUID{}, "shared-folder", true)
	privateFolder := makeNode(t, ctx, h.q, userBID, pgtype.UUID{}, "private-folder", true)

	// Song nodes inside each folder.
	sharedSongNode := makeNode(t, ctx, h.q, userBID, sharedFolder, "shared.mp3", false)
	privateSongNode := makeNode(t, ctx, h.q, userBID, privateFolder, "private.mp3", false)

	// Seed B's songs, referencing these nodes.
	seed(t, ctx, h.q, userBID, sharedSongNode, "B-Shared-Artist", "B-Shared-Album", "B-Shared-Song")
	seed(t, ctx, h.q, userBID, privateSongNode, "B-Private-Artist", "B-Private-Album", "B-Private-Song")

	// Share sharedFolder from B to A.
	if _, err := h.q.CreateShare(ctx, db.CreateShareParams{
		ResourceType:   "file_node",
		ResourceID:     sharedFolder,
		OwnerID:        userBID,
		SharedWithUser: userAID,
		Access:         "read",
	}); err != nil {
		t.Fatalf("CreateShare: %v", err)
	}

	// User A calls getArtists.
	resp := doGet(h, testAPIKey, "getArtists", "")
	if resp == nil || resp["status"] != "ok" {
		t.Fatalf("getArtists failed: %v", resp)
	}

	names := artistNames(resp, "artists")

	if !names["A-Artist"] {
		t.Errorf("A-Artist missing from results: %v", names)
	}
	if !names["B-Shared-Artist"] {
		t.Errorf("B-Shared-Artist missing (should be visible via share): %v", names)
	}
	if names["B-Private-Artist"] {
		t.Errorf("B-Private-Artist should NOT be visible to A, but appears in: %v", names)
	}
}

// getArtists reports each artist's own number of albums (counted for all artists in one
// query, not one query per artist).
func TestGetArtistsAlbumCounts(t *testing.T) {
	h, ctx, userAID, _ := setupTwo(t)
	for i, s := range [][3]string{
		{"Arctic Monkeys", "AM", "Do I Wanna Know?"},
		{"Arctic Monkeys", "AM", "R U Mine?"},
		{"Arctic Monkeys", "Humbug", "Crying Lightning"},
		{"Blur", "Parklife", "Girls & Boys"},
	} {
		n := makeNode(t, ctx, h.q, userAID, pgtype.UUID{}, fmt.Sprintf("s%d.mp3", i), false)
		seed(t, ctx, h.q, userAID, n, s[0], s[1], s[2])
	}
	resp := doGet(h, testAPIKey, "getArtists", "")
	if resp == nil || resp["status"] != "ok" {
		t.Fatalf("getArtists failed: %v", resp)
	}
	got := map[string]float64{}
	wrapper, _ := resp["artists"].(map[string]any)
	index, _ := wrapper["index"].([]any)
	for _, group := range index {
		g, _ := group.(map[string]any)
		for _, a := range g["artist"].([]any) {
			ao, _ := a.(map[string]any)
			got[ao["name"].(string)], _ = ao["albumCount"].(float64)
		}
	}
	if got["Arctic Monkeys"] != 2 || got["Blur"] != 1 {
		t.Errorf("albumCount = %v, want Arctic Monkeys 2 and Blur 1", got)
	}
}
