package subsonic

import (
	"context"
	"strconv"

	"discodrive/internal/db"

	"github.com/jackc/pgx/v5/pgtype"
)

func init() {
	endpoints["star"] = star
	endpoints["unstar"] = unstar
	endpoints["setRating"] = setRating
	endpoints["getStarred"] = getStarred
	endpoints["getStarred2"] = getStarred2
}

// starOp handles both star and unstar for repeated id / albumId / artistId params.
func starOp(h *Handler, c *reqCtx, add bool) {
	ctx := c.r.Context()
	userUUID, err := db.ParseUUID(c.userID)
	if err != nil {
		c.fail(ErrGeneric, "invalid user id")
		return
	}

	// id can refer to any type; decode kind from prefix.
	for _, rawID := range c.paramList("id") {
		kind, uuid, ok := decID(rawID)
		if !ok {
			continue
		}
		itemUUID, err := db.ParseUUID(uuid)
		if err != nil {
			continue
		}
		var itemType string
		switch kind {
		case "tr":
			itemType = "song"
		case "al":
			itemType = "album"
		case "ar":
			itemType = "artist"
		default:
			continue
		}
		applyStarOp(ctx, h, userUUID, itemUUID, itemType, add)
	}

	// albumId always means album.
	for _, rawID := range c.paramList("albumId") {
		_, uuid, ok := decID(rawID)
		if !ok {
			continue
		}
		itemUUID, err := db.ParseUUID(uuid)
		if err != nil {
			continue
		}
		applyStarOp(ctx, h, userUUID, itemUUID, "album", add)
	}

	// artistId always means artist.
	for _, rawID := range c.paramList("artistId") {
		_, uuid, ok := decID(rawID)
		if !ok {
			continue
		}
		itemUUID, err := db.ParseUUID(uuid)
		if err != nil {
			continue
		}
		applyStarOp(ctx, h, userUUID, itemUUID, "artist", add)
	}

	c.ok(map[string]any{})
}

// applyStarOp writes or removes a star for the given item.
// For add=true the item must be accessible to the user; inaccessible items are
// silently skipped so that Subsonic clients that batch-star multiple ids still
// receive an "ok" response for the accessible subset.
// For add=false (unstar) no accessibility check is needed: removing a star row
// that the caller owns is always safe.
func applyStarOp(ctx context.Context, h *Handler, userUUID, itemUUID pgtype.UUID, itemType string, add bool) {
	if add {
		// Gate write on accessibility to prevent cross-user metadata leaks.
		switch itemType {
		case "song":
			if _, err := h.q.AccessibleSong(ctx, db.AccessibleSongParams{UserID: userUUID, ID: itemUUID}); err != nil {
				return
			}
		case "album":
			if _, err := h.q.AccessibleAlbum(ctx, db.AccessibleAlbumParams{UserID: userUUID, ID: itemUUID}); err != nil {
				return
			}
		case "artist":
			if _, err := h.q.AccessibleArtist(ctx, db.AccessibleArtistParams{UserID: userUUID, ID: itemUUID}); err != nil {
				return
			}
		}
		_ = h.q.Star(ctx, db.StarParams{UserID: userUUID, ItemID: itemUUID, ItemType: itemType})
	} else {
		_ = h.q.Unstar(ctx, db.UnstarParams{UserID: userUUID, ItemID: itemUUID, ItemType: itemType})
	}
}

func star(h *Handler, c *reqCtx)   { starOp(h, c, true) }
func unstar(h *Handler, c *reqCtx) { starOp(h, c, false) }

// setRating stores or removes a rating for an item.
func setRating(h *Handler, c *reqCtx) {
	ctx := c.r.Context()
	userUUID, err := db.ParseUUID(c.userID)
	if err != nil {
		c.fail(ErrGeneric, "invalid user id")
		return
	}

	ratingStr := c.param("rating")
	rating, err := strconv.Atoi(ratingStr)
	if err != nil || rating < 0 || rating > 5 {
		c.fail(ErrMissingParam, "rating must be 0..5")
		return
	}

	kind, uuid, ok := decID(c.param("id"))
	if !ok {
		c.fail(ErrNotFound, "item not found")
		return
	}
	itemUUID, err := db.ParseUUID(uuid)
	if err != nil {
		c.fail(ErrNotFound, "item not found")
		return
	}

	var itemType string
	switch kind {
	case "tr":
		itemType = "song"
	case "al":
		itemType = "album"
	case "ar":
		itemType = "artist"
	default:
		c.fail(ErrNotFound, "item not found")
		return
	}

	if rating == 0 {
		// rating=0 removes; no accessibility check needed (removing own data is safe).
		_ = h.q.DeleteRating(ctx, db.DeleteRatingParams{
			UserID:   userUUID,
			ItemID:   itemUUID,
			ItemType: itemType,
		})
	} else {
		// Gate write: only rate items the caller can access.
		var accessible bool
		switch itemType {
		case "song":
			_, err := h.q.AccessibleSong(ctx, db.AccessibleSongParams{UserID: userUUID, ID: itemUUID})
			accessible = err == nil
		case "album":
			_, err := h.q.AccessibleAlbum(ctx, db.AccessibleAlbumParams{UserID: userUUID, ID: itemUUID})
			accessible = err == nil
		case "artist":
			_, err := h.q.AccessibleArtist(ctx, db.AccessibleArtistParams{UserID: userUUID, ID: itemUUID})
			accessible = err == nil
		}
		if accessible {
			_ = h.q.SetRating(ctx, db.SetRatingParams{
				UserID:   userUUID,
				ItemID:   itemUUID,
				ItemType: itemType,
				Rating:   int32(rating),
			})
		}
	}

	c.ok(map[string]any{})
}

// buildStarredPayload assembles the starred/starred2 inner object.
// userIDStr is the string user ID used to load marks (starred_at + ratings).
func buildStarredPayload(ctx context.Context, h *Handler, userUUID pgtype.UUID, userIDStr string) map[string]any {
	marks := h.loadMarks(ctx, userIDStr)

	starredSongs, _ := h.q.ListStarredSongs(ctx, userUUID)
	songChildren := make([]map[string]any, 0, len(starredSongs))
	albums := h.albumsOf(ctx, starredSongs)
	for _, s := range starredSongs {
		alb := albums[s.AlbumID]
		sUUID := db.UUIDString(s.ID)
		songChildren = append(songChildren, buildSongChild(s, alb.Name, alb.ArtistName, alb.Year,
			marks.starredAt("song", sUUID), marks.ratingOf("song", sUUID)))
	}

	starredAlbums, _ := h.q.ListStarredAlbums(ctx, userUUID)
	albumObjs := make([]map[string]any, 0, len(starredAlbums))
	for _, al := range starredAlbums {
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
		// Items returned by getStarred are always starred; populate from marks.
		if s := marks.starredAt("album", alUUID); s != "" {
			obj["starred"] = s
		}
		if r := marks.ratingOf("album", alUUID); r > 0 {
			obj["userRating"] = r
		}
		albumObjs = append(albumObjs, obj)
	}

	starredArtists, _ := h.q.ListStarredArtists(ctx, userUUID)
	artistObjs := make([]map[string]any, 0, len(starredArtists))
	for _, a := range starredArtists {
		aUUID := db.UUIDString(a.ID)
		obj := map[string]any{
			"id":       encID("ar", aUUID),
			"name":     a.Name,
			"coverArt": encID("ar", aUUID),
		}
		if a.MusicbrainzID.Valid {
			obj["musicBrainzId"] = a.MusicbrainzID.String
		}
		// Items returned by getStarred are always starred; populate from marks.
		if s := marks.starredAt("artist", aUUID); s != "" {
			obj["starred"] = s
		}
		if r := marks.ratingOf("artist", aUUID); r > 0 {
			obj["userRating"] = r
		}
		artistObjs = append(artistObjs, obj)
	}

	return map[string]any{
		"artist": artistObjs,
		"album":  albumObjs,
		"song":   songChildren,
	}
}

// getStarred returns starred items (v1-style wrapper key "starred").
func getStarred(h *Handler, c *reqCtx) {
	ctx := c.r.Context()
	userUUID, err := db.ParseUUID(c.userID)
	if err != nil {
		c.fail(ErrGeneric, "invalid user id")
		return
	}
	c.ok(map[string]any{"starred": buildStarredPayload(ctx, h, userUUID, c.userID)})
}

// getStarred2 returns starred items (v2-style wrapper key "starred2").
func getStarred2(h *Handler, c *reqCtx) {
	ctx := c.r.Context()
	userUUID, err := db.ParseUUID(c.userID)
	if err != nil {
		c.fail(ErrGeneric, "invalid user id")
		return
	}
	c.ok(map[string]any{"starred2": buildStarredPayload(ctx, h, userUUID, c.userID)})
}
