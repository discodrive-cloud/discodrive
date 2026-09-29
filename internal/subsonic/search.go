package subsonic

import (
	"context"
	"strconv"
	"strings"

	"discodrive/internal/db"

	"github.com/jackc/pgx/v5/pgtype"
)

func init() {
	endpoints["search3"] = search3
	endpoints["search2"] = search2
}

// parseSearchIntParam parses an integer query parameter with a default value.
// Returns the default if the parameter is missing or invalid.
func parseSearchIntParam(c *reqCtx, name string, defaultVal int) int32 {
	s := c.param(name)
	if s == "" {
		return int32(defaultVal)
	}
	v, err := strconv.Atoi(s)
	if err != nil || v < 0 {
		return int32(defaultVal)
	}
	return int32(v)
}

// runSearch executes all three search queries (artists, albums, songs) and writes
// the Subsonic response. wrapKey is either "searchResult3" or "searchResult2".
func runSearch(h *Handler, c *reqCtx, wrapKey string) {
	ctx := context.Background()
	userUUID, err := db.ParseUUID(c.userID)
	if err != nil {
		c.fail(ErrGeneric, "invalid user id")
		return
	}

	// Blank/whitespace query means "match everything": ILIKE '%%' matches all rows.
	q := strings.TrimSpace(c.param("query"))

	artistCount := parseSearchIntParam(c, "artistCount", 20)
	artistOffset := parseSearchIntParam(c, "artistOffset", 0)
	albumCount := parseSearchIntParam(c, "albumCount", 20)
	albumOffset := parseSearchIntParam(c, "albumOffset", 0)
	songCount := parseSearchIntParam(c, "songCount", 20)
	songOffset := parseSearchIntParam(c, "songOffset", 0)

	// Artists.
	artists, err := h.q.SearchArtists(ctx, db.SearchArtistsParams{
		UserID: userUUID,
		Query:  pgtype.Text{String: db.EscapeLike(q), Valid: true},
		Lim:    artistCount,
		Off:    artistOffset,
	})
	if err != nil {
		c.fail(ErrGeneric, "database error")
		return
	}

	marks := h.loadMarks(ctx, c.userID)

	artistObjs := make([]map[string]any, 0, len(artists))
	for _, a := range artists {
		cnt, _ := h.q.CountAccessibleAlbumsByArtist(ctx, db.CountAccessibleAlbumsByArtistParams{
			UserID:   userUUID,
			ArtistID: a.ID,
		})
		aUUID := db.UUIDString(a.ID)
		obj := map[string]any{
			"id":         encID("ar", aUUID),
			"name":       a.Name,
			"albumCount": cnt,
			"coverArt":   encID("ar", aUUID),
		}
		if a.MusicbrainzID.Valid {
			obj["musicBrainzId"] = a.MusicbrainzID.String
		}
		if s := marks.starredAt("artist", aUUID); s != "" {
			obj["starred"] = s
		}
		if r := marks.ratingOf("artist", aUUID); r > 0 {
			obj["userRating"] = r
		}
		artistObjs = append(artistObjs, obj)
	}

	// Albums.
	albums, err := h.q.SearchAlbums(ctx, db.SearchAlbumsParams{
		UserID: userUUID,
		Query:  pgtype.Text{String: db.EscapeLike(q), Valid: true},
		Lim:    albumCount,
		Off:    albumOffset,
	})
	if err != nil {
		c.fail(ErrGeneric, "database error")
		return
	}

	albumObjs := make([]map[string]any, 0, len(albums))
	for _, al := range albums {
		alUUID := db.UUIDString(al.ID)
		obj := map[string]any{
			"id":        encID("al", alUUID),
			"name":      al.Name,
			"artist":    al.ArtistName,
			"artistId":  encID("ar", db.UUIDString(al.ArtistID)),
			"coverArt":  encID("al", alUUID),
			"songCount": al.SongCount,
		}
		if al.Year.Valid {
			obj["year"] = al.Year.Int32
		}
		if al.Genre.Valid {
			obj["genre"] = al.Genre.String
		}
		if al.MusicbrainzID.Valid {
			obj["musicBrainzId"] = al.MusicbrainzID.String
		}
		if s := marks.starredAt("album", alUUID); s != "" {
			obj["starred"] = s
		}
		if r := marks.ratingOf("album", alUUID); r > 0 {
			obj["userRating"] = r
		}
		albumObjs = append(albumObjs, obj)
	}

	// Songs.
	songs, err := h.q.SearchSongs(ctx, db.SearchSongsParams{
		UserID: userUUID,
		Query:  pgtype.Text{String: db.EscapeLike(q), Valid: true},
		Lim:    songCount,
		Off:    songOffset,
	})
	if err != nil {
		c.fail(ErrGeneric, "database error")
		return
	}

	songObjs := make([]map[string]any, 0, len(songs))
	for _, s := range songs {
		songObjs = append(songObjs, searchSongRowToChild(s, marks))
	}

	c.ok(map[string]any{
		wrapKey: map[string]any{
			"artist": artistObjs,
			"album":  albumObjs,
			"song":   songObjs,
		},
	})
}

// search3 implements the OpenSubsonic search3 endpoint (ID3-style artist/album/song results).
func search3(h *Handler, c *reqCtx) {
	runSearch(h, c, "searchResult3")
}

// search2 implements the legacy Subsonic search2 endpoint (same data, different wrapper key).
func search2(h *Handler, c *reqCtx) {
	runSearch(h, c, "searchResult2")
}
