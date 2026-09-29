package subsonic

import (
	"context"
	"time"

	"discodrive/internal/db"

	"github.com/jackc/pgx/v5/pgtype"
)

func init() {
	endpoints["getRandomSongs"] = getRandomSongs
	endpoints["getSongsByGenre"] = getSongsByGenre
	endpoints["getNowPlaying"] = getNowPlaying
}

// getRandomSongs implements the Subsonic getRandomSongs endpoint.
// Params: size (def 10, max 500), genre (optional), fromYear (optional), toYear (optional).
func getRandomSongs(h *Handler, c *reqCtx) {
	ctx := context.Background()
	userUUID, err := db.ParseUUID(c.userID)
	if err != nil {
		c.fail(ErrGeneric, "invalid user id")
		return
	}

	size := parseCountParam(c, "size", 10)
	if size > 500 {
		size = 500
	}
	if size < 1 {
		size = 1
	}

	// Optional genre filter.
	var genre pgtype.Text
	if g := c.param("genre"); g != "" {
		genre = pgtype.Text{String: g, Valid: true}
	}

	// Optional year filters.
	var fromYear, toYear pgtype.Int4
	if v, ok := parseInt32Param(c, "fromYear"); ok {
		fromYear = pgtype.Int4{Int32: v, Valid: true}
	}
	if v, ok := parseInt32Param(c, "toYear"); ok {
		toYear = pgtype.Int4{Int32: v, Valid: true}
	}

	rows, err := h.q.RandomAccessibleSongs(ctx, db.RandomAccessibleSongsParams{
		UserID:   userUUID,
		Genre:    genre,
		FromYear: fromYear,
		ToYear:   toYear,
		Lim:      size,
	})
	if err != nil {
		c.fail(ErrGeneric, "database error")
		return
	}

	marks := h.loadMarks(ctx, c.userID)
	songs := make([]map[string]any, 0, len(rows))
	for _, s := range rows {
		songs = append(songs, randomSongRowToChild(s, marks))
	}

	c.ok(map[string]any{
		"randomSongs": map[string]any{"song": songs},
	})
}

// getSongsByGenre implements the Subsonic getSongsByGenre endpoint.
// Param genre is required; missing genre returns error code 10.
func getSongsByGenre(h *Handler, c *reqCtx) {
	genre := c.param("genre")
	if genre == "" {
		c.fail(ErrMissingParam, "Required parameter 'genre' is missing")
		return
	}

	ctx := context.Background()
	userUUID, err := db.ParseUUID(c.userID)
	if err != nil {
		c.fail(ErrGeneric, "invalid user id")
		return
	}

	count := parseCountParam(c, "count", 10)
	offset := parseSearchIntParam(c, "offset", 0)

	rows, err := h.q.SongsByGenre(ctx, db.SongsByGenreParams{
		UserID: userUUID,
		Genre:  pgtype.Text{String: genre, Valid: true},
		Lim:    count,
		Off:    offset,
	})
	if err != nil {
		c.fail(ErrGeneric, "database error")
		return
	}

	marks := h.loadMarks(ctx, c.userID)
	songs := make([]map[string]any, 0, len(rows))
	for _, s := range rows {
		songs = append(songs, genreSongRowToChild(s, marks))
	}

	c.ok(map[string]any{
		"songsByGenre": map[string]any{"song": songs},
	})
}

// getNowPlaying implements the Subsonic getNowPlaying endpoint.
// Returns up to 20 most recently played songs by the calling user.
func getNowPlaying(h *Handler, c *reqCtx) {
	ctx := context.Background()
	userUUID, err := db.ParseUUID(c.userID)
	if err != nil {
		c.fail(ErrGeneric, "invalid user id")
		return
	}

	rows, err := h.q.RecentPlayHistory(ctx, db.RecentPlayHistoryParams{
		UserID: userUUID,
		Lim:    20,
	})
	if err != nil {
		c.fail(ErrGeneric, "database error")
		return
	}

	// Resolve the login name once; fall back to the UUID string if the lookup fails.
	username := c.userID
	if u, err2 := h.q.GetUserByID(ctx, userUUID); err2 == nil {
		username = u.Email
	}

	marks := h.loadMarks(ctx, c.userID)
	entries := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		entry := recentPlayRowToChild(r, marks)
		// getNowPlaying entries also carry username, minutesAgo, and playerId.
		entry["username"] = username
		entry["playerId"] = 0
		var minutesAgo int64
		if r.PlayedAt.Valid {
			minutesAgo = int64(time.Since(r.PlayedAt.Time).Minutes())
		}
		entry["minutesAgo"] = minutesAgo
		entries = append(entries, entry)
	}

	c.ok(map[string]any{
		"nowPlaying": map[string]any{"entry": entries},
	})
}
