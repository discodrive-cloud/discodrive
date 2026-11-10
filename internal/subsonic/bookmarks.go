package subsonic

import (
	"context"
	"github.com/jackc/pgx/v5/pgtype"
	"strconv"
	"time"

	"discodrive/internal/db"
)

func init() {
	endpoints["getBookmarks"] = getBookmarks
	endpoints["createBookmark"] = createBookmark
	endpoints["deleteBookmark"] = deleteBookmark
}

// itemTypeFromKind maps an id prefix to its bookmark item_type.
// "tr" -> "song", "pe" -> "episode". ok is false for any other kind.
func itemTypeFromKind(kind string) (itemType string, ok bool) {
	switch kind {
	case "tr":
		return "song", true
	case "pe":
		return "episode", true
	default:
		return "", false
	}
}

func createBookmark(h *Handler, c *reqCtx) {
	ctx := context.Background()

	userUUID, err := db.ParseUUID(c.userID)
	if err != nil {
		c.fail(ErrGeneric, "invalid user id")
		return
	}

	id := c.param("id")
	if id == "" {
		c.fail(ErrMissingParam, "Required parameter 'id' is missing")
		return
	}

	kind, uuidStr, ok := decID(id)
	if !ok {
		c.fail(ErrNotFound, "item not found")
		return
	}
	itemType, ok := itemTypeFromKind(kind)
	if !ok {
		c.fail(ErrNotFound, "item not found")
		return
	}

	itemUUID, err := db.ParseUUID(uuidStr)
	if err != nil {
		c.fail(ErrNotFound, "item not found")
		return
	}

	// Only items the user can play may be bookmarked: an arbitrary id would
	// otherwise leave rows pointing at other users' songs and episodes.
	switch itemType {
	case "song":
		_, err = h.q.AccessibleSong(ctx, db.AccessibleSongParams{UserID: userUUID, ID: itemUUID})
	case "episode":
		_, err = h.q.GetEpisodeForUser(ctx, db.GetEpisodeForUserParams{ID: itemUUID, UserID: userUUID})
	}
	if err != nil {
		c.fail(ErrNotFound, "item not found")
		return
	}

	position, _ := strconv.ParseInt(c.param("position"), 10, 64)

	if err := h.q.CreateBookmark(ctx, db.CreateBookmarkParams{
		UserID:     userUUID,
		ItemID:     itemUUID,
		ItemType:   itemType,
		PositionMs: position,
		Comment:    c.param("comment"),
	}); err != nil {
		c.fail(ErrGeneric, "database error")
		return
	}

	c.ok(map[string]any{})
}

func deleteBookmark(h *Handler, c *reqCtx) {
	ctx := context.Background()

	userUUID, err := db.ParseUUID(c.userID)
	if err != nil {
		c.fail(ErrGeneric, "invalid user id")
		return
	}

	id := c.param("id")
	if id == "" {
		c.fail(ErrMissingParam, "Required parameter 'id' is missing")
		return
	}

	kind, uuidStr, ok := decID(id)
	if !ok {
		c.fail(ErrNotFound, "item not found")
		return
	}
	itemType, ok := itemTypeFromKind(kind)
	if !ok {
		c.fail(ErrNotFound, "item not found")
		return
	}

	itemUUID, err := db.ParseUUID(uuidStr)
	if err != nil {
		c.fail(ErrNotFound, "item not found")
		return
	}

	// Lenient: deleting a non-existent bookmark is a no-op success.
	if err := h.q.DeleteBookmark(ctx, db.DeleteBookmarkParams{
		UserID:   userUUID,
		ItemID:   itemUUID,
		ItemType: itemType,
	}); err != nil {
		c.fail(ErrGeneric, "database error")
		return
	}

	c.ok(map[string]any{})
}

func getBookmarks(h *Handler, c *reqCtx) {
	ctx := context.Background()

	userUUID, err := db.ParseUUID(c.userID)
	if err != nil {
		c.fail(ErrGeneric, "invalid user id")
		return
	}

	rows, err := h.q.ListBookmarks(ctx, userUUID)
	if err != nil {
		c.fail(ErrGeneric, "database error")
		return
	}

	// Resolve the username once (Subsonic bookmarks carry the owner's username).
	var username string
	if u, err := h.q.GetUserByID(ctx, userUUID); err == nil {
		username = u.Email
	}

	marks := h.loadMarks(ctx, c.userID)

	// The items the bookmarks point at, read together: a song and its album used to cost
	// two queries a bookmark, an episode one.
	var songIDs, episodeIDs []pgtype.UUID
	for _, bm := range rows {
		switch bm.ItemType {
		case "song":
			songIDs = append(songIDs, bm.ItemID)
		case "episode":
			episodeIDs = append(episodeIDs, bm.ItemID)
		}
	}
	songs := map[pgtype.UUID]db.Song{}
	var albums map[pgtype.UUID]db.AlbumsWithArtistRow
	if len(songIDs) > 0 {
		list, err := h.q.AccessibleSongsByIDs(ctx, db.AccessibleSongsByIDsParams{UserID: userUUID, Ids: songIDs})
		if err != nil {
			c.fail(ErrGeneric, "database error")
			return
		}
		for _, s := range list {
			songs[s.ID] = s
		}
		albums = h.albumsOf(ctx, list)
	}
	episodes := map[pgtype.UUID]db.PodcastEpisode{}
	if len(episodeIDs) > 0 {
		list, err := h.q.GetEpisodesForUser(ctx, db.GetEpisodesForUserParams{UserID: userUUID, Ids: episodeIDs})
		if err != nil {
			c.fail(ErrGeneric, "database error")
			return
		}
		for _, e := range list {
			episodes[e.ID] = e
		}
	}

	bookmarks := make([]any, 0, len(rows))
	for _, bm := range rows {
		uuidStr := db.UUIDString(bm.ItemID)

		var entry map[string]any
		switch bm.ItemType {
		case "song":
			song, found := songs[bm.ItemID]
			if !found {
				// Item gone or no longer accessible: skip this bookmark.
				continue
			}
			album, found := albums[song.AlbumID]
			if !found {
				continue
			}
			entry = buildSongChild(song, album.Name, album.ArtistName, album.Year,
				marks.starredAt("song", uuidStr), marks.ratingOf("song", uuidStr))
		case "episode":
			ep, found := episodes[bm.ItemID]
			if !found {
				continue
			}
			entry = episodeChild(ep)
		default:
			continue
		}

		obj := map[string]any{
			"position": bm.PositionMs,
			"username": username,
			"comment":  bm.Comment,
			"entry":    entry,
		}
		if bm.CreatedAt.Valid {
			obj["created"] = bm.CreatedAt.Time.UTC().Format(time.RFC3339)
		}
		if bm.ChangedAt.Valid {
			obj["changed"] = bm.ChangedAt.Time.UTC().Format(time.RFC3339)
		}
		bookmarks = append(bookmarks, obj)
	}

	c.ok(map[string]any{
		"bookmarks": map[string]any{
			"bookmark": bookmarks,
		},
	})
}
