package subsonic

import (
	"context"

	"discodrive/internal/db"

	"github.com/jackc/pgx/v5/pgtype"
)

func init() {
	endpoints["getAlbumList2"] = getAlbumList2
	endpoints["getAlbumList"] = getAlbumList
	endpoints["getGenres"] = getGenres
}

// albumListEntry is a normalized album row used internally for building responses.
// It carries only the fields that all query result types share.
type albumListEntry struct {
	ID            pgtype.UUID
	ArtistID      pgtype.UUID
	Name          string
	ArtistName    string
	Year          pgtype.Int4
	Genre         pgtype.Text
	SongCount     int32
	TotalDuration int64
}

// albumObj builds the canonical Subsonic album object for list responses.
// marks is used to populate starred/userRating for the album.
func albumObj(e albumListEntry, marks userMarks) map[string]any {
	uuid := db.UUIDString(e.ID)
	obj := map[string]any{
		"id":        encID("al", uuid),
		"name":      e.Name,
		"artist":    e.ArtistName,
		"artistId":  encID("ar", db.UUIDString(e.ArtistID)),
		"coverArt":  encID("al", uuid),
		"songCount": e.SongCount,
		"duration":  e.TotalDuration,
	}
	if e.Year.Valid {
		obj["year"] = e.Year.Int32
	}
	if e.Genre.Valid {
		obj["genre"] = e.Genre.String
	}
	if s := marks.starredAt("album", uuid); s != "" {
		obj["starred"] = s
	}
	if r := marks.ratingOf("album", uuid); r > 0 {
		obj["userRating"] = r
	}
	return obj
}

// parseListParams parses the common pagination parameters for album list endpoints.
func parseListParams(c *reqCtx) (size, offset int32) {
	size = min(parseSearchIntParam(c, "size", 10), 500)
	if size == 0 {
		size = 10
	}
	return size, parseSearchIntParam(c, "offset", 0)
}

// fetchAlbumList fetches albums according to the requested type and returns them
// as a slice of albumListEntry. Returns nil,nil for an empty result.
func fetchAlbumList(ctx context.Context, h *Handler, userUUID pgtype.UUID, c *reqCtx) ([]albumListEntry, error) {
	size, offset := parseListParams(c)
	kind := c.param("type")
	if kind == "" {
		kind = "newest"
	}

	switch kind {
	case "alphabeticalByName":
		rows, err := h.q.AccessibleAlbumListAlpha(ctx, db.AccessibleAlbumListAlphaParams{
			UserID: userUUID, Limit: size, Offset: offset,
		})
		if err != nil {
			return nil, err
		}
		out := make([]albumListEntry, len(rows))
		for i, r := range rows {
			out[i] = albumListEntry{ID: r.ID, ArtistID: r.ArtistID, Name: r.Name,
				ArtistName: r.ArtistName, Year: r.Year, Genre: r.Genre,
				SongCount: r.SongCount, TotalDuration: r.TotalDuration}
		}
		return out, nil

	case "byYear":
		fromYear := int32(0)
		toYear := int32(9999)
		if v, ok := parseInt32Param(c, "fromYear"); ok {
			fromYear = v
		}
		if v, ok := parseInt32Param(c, "toYear"); ok {
			toYear = v
		}
		// When fromYear > toYear, use descending order (as per OpenSubsonic spec).
		if fromYear > toYear {
			fromYear, toYear = toYear, fromYear
			rows, err := h.q.AccessibleAlbumListByYearDesc(ctx, db.AccessibleAlbumListByYearDescParams{
				UserID: userUUID, Limit: size, Offset: offset,
				Year:   pgtype.Int4{Int32: fromYear, Valid: true},
				Year_2: pgtype.Int4{Int32: toYear, Valid: true},
			})
			if err != nil {
				return nil, err
			}
			out := make([]albumListEntry, len(rows))
			for i, r := range rows {
				out[i] = albumListEntry{ID: r.ID, ArtistID: r.ArtistID, Name: r.Name,
					ArtistName: r.ArtistName, Year: r.Year, Genre: r.Genre,
					SongCount: r.SongCount, TotalDuration: r.TotalDuration}
			}
			return out, nil
		}
		rows, err := h.q.AccessibleAlbumListByYearAsc(ctx, db.AccessibleAlbumListByYearAscParams{
			UserID: userUUID, Limit: size, Offset: offset,
			Year:   pgtype.Int4{Int32: fromYear, Valid: true},
			Year_2: pgtype.Int4{Int32: toYear, Valid: true},
		})
		if err != nil {
			return nil, err
		}
		out := make([]albumListEntry, len(rows))
		for i, r := range rows {
			out[i] = albumListEntry{ID: r.ID, ArtistID: r.ArtistID, Name: r.Name,
				ArtistName: r.ArtistName, Year: r.Year, Genre: r.Genre,
				SongCount: r.SongCount, TotalDuration: r.TotalDuration}
		}
		return out, nil

	case "byGenre":
		genreStr := c.param("genre")
		rows, err := h.q.AccessibleAlbumListByGenre(ctx, db.AccessibleAlbumListByGenreParams{
			UserID: userUUID, Limit: size, Offset: offset,
			Genre: pgtype.Text{String: genreStr, Valid: true},
		})
		if err != nil {
			return nil, err
		}
		out := make([]albumListEntry, len(rows))
		for i, r := range rows {
			out[i] = albumListEntry{ID: r.ID, ArtistID: r.ArtistID, Name: r.Name,
				ArtistName: r.ArtistName, Year: r.Year, Genre: r.Genre,
				SongCount: r.SongCount, TotalDuration: r.TotalDuration}
		}
		return out, nil

	case "random":
		rows, err := h.q.AccessibleAlbumListRandom(ctx, db.AccessibleAlbumListRandomParams{
			UserID: userUUID, Limit: size, Offset: offset,
		})
		if err != nil {
			return nil, err
		}
		out := make([]albumListEntry, len(rows))
		for i, r := range rows {
			out[i] = albumListEntry{ID: r.ID, ArtistID: r.ArtistID, Name: r.Name,
				ArtistName: r.ArtistName, Year: r.Year, Genre: r.Genre,
				SongCount: r.SongCount, TotalDuration: r.TotalDuration}
		}
		return out, nil

	case "recent":
		rows, err := h.q.AccessibleAlbumListRecent(ctx, db.AccessibleAlbumListRecentParams{
			UserID: userUUID, Limit: size, Offset: offset,
		})
		if err != nil {
			return nil, err
		}
		out := make([]albumListEntry, len(rows))
		for i, r := range rows {
			out[i] = albumListEntry{ID: r.ID, ArtistID: r.ArtistID, Name: r.Name,
				ArtistName: r.ArtistName, Year: r.Year, Genre: r.Genre,
				SongCount: r.SongCount, TotalDuration: r.TotalDuration}
		}
		return out, nil

	case "frequent":
		rows, err := h.q.AccessibleAlbumListFrequent(ctx, db.AccessibleAlbumListFrequentParams{
			UserID: userUUID, Limit: size, Offset: offset,
		})
		if err != nil {
			return nil, err
		}
		out := make([]albumListEntry, len(rows))
		for i, r := range rows {
			out[i] = albumListEntry{ID: r.ID, ArtistID: r.ArtistID, Name: r.Name,
				ArtistName: r.ArtistName, Year: r.Year, Genre: r.Genre,
				SongCount: r.SongCount, TotalDuration: r.TotalDuration}
		}
		return out, nil

	default: // "newest" and any unknown type
		rows, err := h.q.AccessibleAlbumListNewest(ctx, db.AccessibleAlbumListNewestParams{
			UserID: userUUID, Limit: size, Offset: offset,
		})
		if err != nil {
			return nil, err
		}
		out := make([]albumListEntry, len(rows))
		for i, r := range rows {
			out[i] = albumListEntry{ID: r.ID, ArtistID: r.ArtistID, Name: r.Name,
				ArtistName: r.ArtistName, Year: r.Year, Genre: r.Genre,
				SongCount: r.SongCount, TotalDuration: r.TotalDuration}
		}
		return out, nil
	}
}

// getAlbumList2 implements the getAlbumList2 Subsonic endpoint (ID3 browsing).
func getAlbumList2(h *Handler, c *reqCtx) {
	ctx := context.Background()
	userUUID, err := db.ParseUUID(c.userID)
	if err != nil {
		c.fail(ErrGeneric, "invalid user id")
		return
	}

	entries, err := fetchAlbumList(ctx, h, userUUID, c)
	if err != nil {
		c.fail(ErrGeneric, "database error")
		return
	}

	marks := h.loadMarks(ctx, c.userID)
	albums := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		albums = append(albums, albumObj(e, marks))
	}
	c.ok(map[string]any{
		"albumList2": map[string]any{"album": albums},
	})
}

// getAlbumList implements the legacy getAlbumList Subsonic endpoint (folder browsing).
// It returns the same data as getAlbumList2 but wrapped in a different key.
func getAlbumList(h *Handler, c *reqCtx) {
	ctx := context.Background()
	userUUID, err := db.ParseUUID(c.userID)
	if err != nil {
		c.fail(ErrGeneric, "invalid user id")
		return
	}

	entries, err := fetchAlbumList(ctx, h, userUUID, c)
	if err != nil {
		c.fail(ErrGeneric, "database error")
		return
	}

	marks := h.loadMarks(ctx, c.userID)
	albums := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		albums = append(albums, albumObj(e, marks))
	}
	c.ok(map[string]any{
		"albumList": map[string]any{"album": albums},
	})
}

// getGenres implements the getGenres Subsonic endpoint.
// Returns all distinct non-empty genres accessible to the user with song and album counts.
func getGenres(h *Handler, c *reqCtx) {
	ctx := context.Background()
	userUUID, err := db.ParseUUID(c.userID)
	if err != nil {
		c.fail(ErrGeneric, "invalid user id")
		return
	}

	rows, err := h.q.AccessibleGenres(ctx, userUUID)
	if err != nil {
		c.fail(ErrGeneric, "database error")
		return
	}

	genres := make([]map[string]any, 0, len(rows))
	for _, g := range rows {
		if !g.Genre.Valid || g.Genre.String == "" {
			continue
		}
		genres = append(genres, map[string]any{
			"value":      g.Genre.String,
			"songCount":  g.SongCount,
			"albumCount": g.AlbumCount,
		})
	}
	c.ok(map[string]any{
		"genres": map[string]any{"genre": genres},
	})
}
