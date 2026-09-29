package subsonic

import (
	"context"
	"strings"

	"discodrive/internal/db"

	"github.com/jackc/pgx/v5/pgtype"
)

func init() {
	endpoints["getArtistInfo"] = getArtistInfo
	endpoints["getArtistInfo2"] = getArtistInfo
	endpoints["getAlbumInfo"] = getAlbumInfo
	endpoints["getAlbumInfo2"] = getAlbumInfo
	endpoints["getSimilarSongs"] = getSimilarSongs
	endpoints["getSimilarSongs2"] = getSimilarSongs
	endpoints["getTopSongs"] = getTopSongs
}

// wantsV2 reports whether the request was for the "v2" variant of an endpoint.
// It strips a trailing ".view" suffix then checks if the path ends with "2".
func wantsV2(c *reqCtx) bool {
	p := c.r.URL.Path
	p = strings.TrimSuffix(p, ".view")
	return strings.HasSuffix(p, "2")
}

// resolveSeed resolves an encoded id (artist/album/song) to an artistID and optional genre.
// Returns ok=false if the id cannot be decoded or the resource is not accessible.
func (h *Handler) resolveSeed(ctx context.Context, userUUID pgtype.UUID, id string) (artistID pgtype.UUID, genre pgtype.Text, ok bool) {
	kind, uuid, decoded := decID(id)
	if !decoded {
		return pgtype.UUID{}, pgtype.Text{}, false
	}
	resourceUUID, err := db.ParseUUID(uuid)
	if err != nil {
		return pgtype.UUID{}, pgtype.Text{}, false
	}

	switch kind {
	case "ar":
		artist, err := h.q.AccessibleArtist(ctx, db.AccessibleArtistParams{
			UserID: userUUID,
			ID:     resourceUUID,
		})
		if err != nil {
			return pgtype.UUID{}, pgtype.Text{}, false
		}
		return artist.ID, pgtype.Text{}, true
	case "al":
		album, err := h.q.AccessibleAlbum(ctx, db.AccessibleAlbumParams{
			UserID: userUUID,
			ID:     resourceUUID,
		})
		if err != nil {
			return pgtype.UUID{}, pgtype.Text{}, false
		}
		return album.ArtistID, album.Genre, true
	case "tr":
		song, err := h.q.AccessibleSong(ctx, db.AccessibleSongParams{
			UserID: userUUID,
			ID:     resourceUUID,
		})
		if err != nil {
			return pgtype.UUID{}, pgtype.Text{}, false
		}
		return song.ArtistID, song.Genre, true
	default:
		return pgtype.UUID{}, pgtype.Text{}, false
	}
}

// getArtistInfo implements both getArtistInfo and getArtistInfo2.
// Returns a stub info object (no external metadata provider) for the requested artist.
func getArtistInfo(h *Handler, c *reqCtx) {
	ctx := context.Background()
	userUUID, err := db.ParseUUID(c.userID)
	if err != nil {
		c.fail(ErrGeneric, "invalid user id")
		return
	}

	kind, uuid, ok := decID(c.param("id"))
	if !ok || kind != "ar" {
		c.fail(ErrNotFound, "artist not found")
		return
	}
	artistUUID, err := db.ParseUUID(uuid)
	if err != nil {
		c.fail(ErrNotFound, "artist not found")
		return
	}

	artist, err := h.q.AccessibleArtist(ctx, db.AccessibleArtistParams{
		UserID: userUUID,
		ID:     artistUUID,
	})
	if err != nil {
		c.fail(ErrNotFound, "artist not found")
		return
	}

	info := map[string]any{
		"biography":      "",
		"lastFmUrl":      "",
		"smallImageUrl":  "",
		"mediumImageUrl": "",
		"largeImageUrl":  "",
		"similarArtist":  []any{},
	}
	if artist.MusicbrainzID.Valid {
		info["musicBrainzId"] = artist.MusicbrainzID.String
	}

	key := "artistInfo"
	if wantsV2(c) {
		key = "artistInfo2"
	}
	c.ok(map[string]any{key: info})
}

// getAlbumInfo implements both getAlbumInfo and getAlbumInfo2.
// Returns a stub info object (no external metadata provider) for the requested album.
func getAlbumInfo(h *Handler, c *reqCtx) {
	ctx := context.Background()
	userUUID, err := db.ParseUUID(c.userID)
	if err != nil {
		c.fail(ErrGeneric, "invalid user id")
		return
	}

	kind, uuid, ok := decID(c.param("id"))
	if !ok || kind != "al" {
		c.fail(ErrNotFound, "album not found")
		return
	}
	albumUUID, err := db.ParseUUID(uuid)
	if err != nil {
		c.fail(ErrNotFound, "album not found")
		return
	}

	album, err := h.q.AccessibleAlbum(ctx, db.AccessibleAlbumParams{
		UserID: userUUID,
		ID:     albumUUID,
	})
	if err != nil {
		c.fail(ErrNotFound, "album not found")
		return
	}

	info := map[string]any{
		"notes":          "",
		"smallImageUrl":  "",
		"mediumImageUrl": "",
		"largeImageUrl":  "",
	}
	if album.MusicbrainzID.Valid {
		info["musicBrainzId"] = album.MusicbrainzID.String
	}

	key := "albumInfo"
	if wantsV2(c) {
		key = "albumInfo2"
	}
	c.ok(map[string]any{key: info})
}

// getSimilarSongs implements both getSimilarSongs and getSimilarSongs2.
// Returns same-artist songs first, then same-genre songs from other artists, capped at count.
func getSimilarSongs(h *Handler, c *reqCtx) {
	ctx := context.Background()
	userUUID, err := db.ParseUUID(c.userID)
	if err != nil {
		c.fail(ErrGeneric, "invalid user id")
		return
	}

	count := parseCountParam(c, "count", 50)

	artistID, genre, ok := h.resolveSeed(ctx, userUUID, c.param("id"))
	if !ok {
		c.fail(ErrNotFound, "song/album/artist not found")
		return
	}

	marks := h.loadMarks(ctx, c.userID)
	songs := make([]map[string]any, 0, count)

	// When the seed has a genre, cap same-artist results at half the requested
	// count so there is room for genre-fill from other artists.
	sameArtistLim := count
	if genre.Valid {
		sameArtistLim = count / 2
		if sameArtistLim < 1 {
			sameArtistLim = 1
		}
	}

	// Same-artist songs first.
	sameArtist, err := h.q.AccessibleSongsByArtist(ctx, db.AccessibleSongsByArtistParams{
		UserID:   userUUID,
		ArtistID: artistID,
		Lim:      sameArtistLim,
	})
	if err != nil {
		c.fail(ErrGeneric, "database error")
		return
	}
	for _, r := range sameArtist {
		songs = append(songs, similarRowToChild(r, marks))
	}

	// Fill remaining slots with same-genre songs from other artists.
	if genre.Valid && int32(len(songs)) < count {
		remaining := count - int32(len(songs))
		genreSongs, err := h.q.SimilarSongsByGenre(ctx, db.SimilarSongsByGenreParams{
			UserID:          userUUID,
			Genre:           genre,
			ExcludeArtistID: artistID,
			Lim:             remaining,
		})
		if err != nil {
			c.fail(ErrGeneric, "database error")
			return
		}
		for _, r := range genreSongs {
			songs = append(songs, similarGenreRowToChild(r, marks))
		}
	}

	key := "similarSongs"
	if wantsV2(c) {
		key = "similarSongs2"
	}
	c.ok(map[string]any{key: map[string]any{"song": songs}})
}

// getTopSongs returns an artist's accessible songs ordered by the caller's play count descending.
func getTopSongs(h *Handler, c *reqCtx) {
	artistName := c.param("artist")
	if artistName == "" {
		c.fail(ErrMissingParam, "Required parameter 'artist' is missing")
		return
	}

	ctx := context.Background()
	userUUID, err := db.ParseUUID(c.userID)
	if err != nil {
		c.fail(ErrGeneric, "invalid user id")
		return
	}

	count := parseCountParam(c, "count", 50)

	rows, err := h.q.TopSongsByArtistName(ctx, db.TopSongsByArtistNameParams{
		UserID:     userUUID,
		ArtistName: artistName,
		Lim:        count,
	})
	if err != nil {
		c.fail(ErrGeneric, "database error")
		return
	}

	marks := h.loadMarks(ctx, c.userID)
	songs := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		songs = append(songs, topSongRowToChild(r, marks))
	}

	c.ok(map[string]any{
		"topSongs": map[string]any{"song": songs},
	})
}
