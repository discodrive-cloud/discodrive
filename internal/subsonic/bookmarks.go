package subsonic

import (
	"context"
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

	bookmarks := make([]any, 0, len(rows))
	for _, bm := range rows {
		uuidStr := db.UUIDString(bm.ItemID)

		var entry map[string]any
		switch bm.ItemType {
		case "song":
			song, err := h.q.AccessibleSong(ctx, db.AccessibleSongParams{
				UserID: userUUID,
				ID:     bm.ItemID,
			})
			if err != nil {
				// Item gone or no longer accessible: skip this bookmark.
				continue
			}
			album, err := h.q.GetAlbumWithArtist(ctx, song.AlbumID)
			if err != nil {
				continue
			}
			entry = buildSongChild(song, album.Name, album.ArtistName, album.Year,
				marks.starredAt("song", uuidStr), marks.ratingOf("song", uuidStr))
		case "episode":
			ep, err := h.q.GetEpisodeForUser(ctx, db.GetEpisodeForUserParams{
				ID:     bm.ItemID,
				UserID: userUUID,
			})
			if err != nil {
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
