package subsonic

import (
	"context"
	"strconv"

	"discodrive/internal/db"

	"github.com/jackc/pgx/v5/pgtype"
)

func init() {
	endpoints["getPlaylists"] = getPlaylists
	endpoints["getPlaylist"] = getPlaylist
	endpoints["createPlaylist"] = createPlaylist
	endpoints["updatePlaylist"] = updatePlaylist
	endpoints["deletePlaylist"] = deletePlaylist
}

// playlistObj builds the Subsonic playlist object (without the entry slice).
func playlistObj(p db.Playlist, songCount int64, duration int64, owner string) map[string]any {
	return map[string]any{
		"id":        encID("pl", db.UUIDString(p.ID)),
		"name":      p.Name,
		"comment":   p.Comment,
		"owner":     owner,
		"public":    p.Public,
		"songCount": songCount,
		"duration":  duration,
		"created":   p.CreatedAt.Time.UTC().Format("2006-01-02T15:04:05"),
		"changed":   p.ChangedAt.Time.UTC().Format("2006-01-02T15:04:05"),
	}
}

// playlistDuration sums the duration of a song slice in seconds.
func playlistDuration(songs []db.Song) int64 {
	var total int64
	for _, s := range songs {
		if s.Duration.Valid {
			total += int64(s.Duration.Int32)
		}
	}
	return total
}

// getPlaylists returns all playlists owned by the calling user.
func getPlaylists(h *Handler, c *reqCtx) {
	ctx := context.Background()
	userUUID, err := db.ParseUUID(c.userID)
	if err != nil {
		c.fail(ErrGeneric, "invalid user id")
		return
	}

	playlists, err := h.q.ListPlaylistsByUser(ctx, userUUID)
	if err != nil {
		c.fail(ErrGeneric, "database error")
		return
	}

	result := make([]map[string]any, 0, len(playlists))
	stats, _ := h.q.PlaylistStatsByUser(ctx, userUUID)
	statsOf := make(map[pgtype.UUID]db.PlaylistStatsByUserRow, len(stats))
	for _, st := range stats {
		statsOf[st.PlaylistID] = st
	}
	for _, p := range playlists {
		st := statsOf[p.ID] // absent = an empty playlist
		result = append(result, playlistObj(p, st.Songs, st.Duration, c.userID))
	}

	c.ok(map[string]any{
		"playlists": map[string]any{"playlist": result},
	})
}

// getPlaylist returns a single playlist (owned by the calling user) with its songs.
func getPlaylist(h *Handler, c *reqCtx) {
	ctx := context.Background()
	userUUID, err := db.ParseUUID(c.userID)
	if err != nil {
		c.fail(ErrGeneric, "invalid user id")
		return
	}

	kind, uuid, ok := decID(c.param("id"))
	if !ok || kind != "pl" {
		c.fail(ErrNotFound, "playlist not found")
		return
	}
	plUUID, err := db.ParseUUID(uuid)
	if err != nil {
		c.fail(ErrNotFound, "playlist not found")
		return
	}

	p, err := h.q.GetPlaylist(ctx, plUUID)
	if err != nil || p.UserID != userUUID {
		c.fail(ErrNotFound, "playlist not found")
		return
	}

	songs, err := h.q.GetPlaylistSongs(ctx, p.ID)
	if err != nil {
		c.fail(ErrGeneric, "database error")
		return
	}

	marks := h.loadMarks(ctx, c.userID)
	entries := make([]map[string]any, 0, len(songs))
	albums := h.albumsOf(ctx, songs)
	for _, s := range songs {
		alb := albums[s.AlbumID]
		sUUID := db.UUIDString(s.ID)
		entries = append(entries, buildSongChild(s, alb.Name, alb.ArtistName, alb.Year,
			marks.starredAt("song", sUUID), marks.ratingOf("song", sUUID)))
	}

	obj := playlistObj(p, int64(len(songs)), playlistDuration(songs), c.userID)
	obj["entry"] = entries

	c.ok(map[string]any{"playlist": obj})
}

// createPlaylist creates a new playlist (or replaces an existing one's songs when
// playlistId is given) and returns it as a getPlaylist response.
func createPlaylist(h *Handler, c *reqCtx) {
	ctx := context.Background()
	userUUID, err := db.ParseUUID(c.userID)
	if err != nil {
		c.fail(ErrGeneric, "invalid user id")
		return
	}

	songIDParams := c.paramList("songId")

	// Subsonic overloads createPlaylist: if playlistId is given, replace that
	// playlist's songs instead of creating a new one.
	if pid := c.param("playlistId"); pid != "" {
		kind, uuid, ok := decID(pid)
		if !ok || kind != "pl" {
			c.fail(ErrNotFound, "playlist not found")
			return
		}
		plUUID, err := db.ParseUUID(uuid)
		if err != nil {
			c.fail(ErrNotFound, "playlist not found")
			return
		}
		p, err := h.q.GetPlaylist(ctx, plUUID)
		if err != nil || p.UserID != userUUID {
			c.fail(ErrNotFound, "playlist not found")
			return
		}
		// Clearing and refilling are one step: an error halfway must not leave the
		// playlist cut short.
		err = h.q.InTx(ctx, func(q *db.Queries) error {
			if _, err := q.LockPlaylist(ctx, p.ID); err != nil {
				return err
			}
			if err := q.ClearPlaylistSongs(ctx, p.ID); err != nil {
				return err
			}
			return insertSongs(ctx, q, userUUID, p.ID, songIDParams, 0)
		})
		if err != nil {
			c.fail(ErrGeneric, "database error")
			return
		}
		servePlaylist(ctx, h, c, p)
		return
	}

	name := c.param("name")
	if name == "" {
		c.fail(ErrMissingParam, "name is required")
		return
	}

	p, err := h.q.CreatePlaylist(ctx, db.CreatePlaylistParams{
		UserID:  userUUID,
		Name:    name,
		Comment: c.param("comment"),
	})
	if err != nil {
		c.fail(ErrGeneric, "database error")
		return
	}

	if err := insertSongs(ctx, h.q, userUUID, p.ID, songIDParams, 0); err != nil {
		c.fail(ErrGeneric, "database error")
		return
	}

	servePlaylist(ctx, h, c, p)
}

// updatePlaylist renames / re-comments a playlist and adds/removes songs by index.
func updatePlaylist(h *Handler, c *reqCtx) {
	ctx := context.Background()
	userUUID, err := db.ParseUUID(c.userID)
	if err != nil {
		c.fail(ErrGeneric, "invalid user id")
		return
	}

	kind, uuid, ok := decID(c.param("playlistId"))
	if !ok || kind != "pl" {
		c.fail(ErrNotFound, "playlist not found")
		return
	}
	plUUID, err := db.ParseUUID(uuid)
	if err != nil {
		c.fail(ErrNotFound, "playlist not found")
		return
	}

	p, err := h.q.GetPlaylist(ctx, plUUID)
	if err != nil || p.UserID != userUUID {
		c.fail(ErrNotFound, "playlist not found")
		return
	}

	// Apply metadata updates (only if params provided, keep current value otherwise).
	newName := c.param("name")
	if newName == "" {
		newName = p.Name
	}
	newComment := p.Comment
	if c.param("comment") != "" {
		newComment = c.param("comment")
	}
	if err := h.q.UpdatePlaylistMeta(ctx, db.UpdatePlaylistMetaParams{
		ID:      p.ID,
		Name:    newName,
		Comment: newComment,
	}); err != nil {
		c.fail(ErrGeneric, "database error")
		return
	}

	// Read, change and write back under a lock on the playlist, in one transaction: two
	// updates at once used to collide on the positions and lose tracks, and an error
	// halfway left the playlist cut short.
	err = h.q.InTx(ctx, func(q *db.Queries) error {
		if _, err := q.LockPlaylist(ctx, p.ID); err != nil {
			return err
		}
		// Load current songs, apply index removals, append additions.
		songs, err := q.GetPlaylistSongs(ctx, p.ID)
		if err != nil {
			return err
		}

		// Parse removal indexes.
		removeIdxSet := map[int]bool{}
		for _, s := range c.paramList("songIndexToRemove") {
			if idx, err := strconv.Atoi(s); err == nil && idx >= 0 && idx < len(songs) {
				removeIdxSet[idx] = true
			}
		}

		// Build surviving ordered list.
		remaining := make([]pgtype.UUID, 0, len(songs))
		for i, s := range songs {
			if !removeIdxSet[i] {
				remaining = append(remaining, s.ID)
			}
		}

		// Append new songs (skip inaccessible).
		for _, rawID := range c.paramList("songIdToAdd") {
			kind, sUUID, ok := decID(rawID)
			if !ok || kind != "tr" {
				continue
			}
			songUUID, err := db.ParseUUID(sUUID)
			if err != nil {
				continue
			}
			if _, err := q.AccessibleSong(ctx, db.AccessibleSongParams{UserID: userUUID, ID: songUUID}); err != nil {
				continue
			}
			remaining = append(remaining, songUUID)
		}

		// Replace playlist songs atomically (clear + re-insert).
		if err := q.ClearPlaylistSongs(ctx, p.ID); err != nil {
			return err
		}
		for i, sid := range remaining {
			if err := q.AddPlaylistSong(ctx, db.AddPlaylistSongParams{
				PlaylistID: p.ID,
				SongID:     sid,
				Position:   int32(i),
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		c.fail(ErrGeneric, "database error")
		return
	}

	c.ok(map[string]any{})
}

// deletePlaylist removes a playlist owned by the calling user.
func deletePlaylist(h *Handler, c *reqCtx) {
	ctx := context.Background()
	userUUID, err := db.ParseUUID(c.userID)
	if err != nil {
		c.fail(ErrGeneric, "invalid user id")
		return
	}

	kind, uuid, ok := decID(c.param("id"))
	if !ok || kind != "pl" {
		c.fail(ErrNotFound, "playlist not found")
		return
	}
	plUUID, err := db.ParseUUID(uuid)
	if err != nil {
		c.fail(ErrNotFound, "playlist not found")
		return
	}

	if err := h.q.DeletePlaylistForUser(ctx, db.DeletePlaylistForUserParams{
		ID:     plUUID,
		UserID: userUUID,
	}); err != nil {
		c.fail(ErrGeneric, "database error")
		return
	}

	c.ok(map[string]any{})
}

// insertSongs inserts accessible songs into a playlist starting at startPos.
func insertSongs(ctx context.Context, q *db.Queries, userUUID, plUUID pgtype.UUID, rawIDs []string, startPos int) error {
	pos := startPos
	for _, rawID := range rawIDs {
		kind, sUUID, ok := decID(rawID)
		if !ok || kind != "tr" {
			continue
		}
		songUUID, err := db.ParseUUID(sUUID)
		if err != nil {
			continue
		}
		if _, err := q.AccessibleSong(ctx, db.AccessibleSongParams{UserID: userUUID, ID: songUUID}); err != nil {
			continue // skip inaccessible songs
		}
		if err := q.AddPlaylistSong(ctx, db.AddPlaylistSongParams{
			PlaylistID: plUUID,
			SongID:     songUUID,
			Position:   int32(pos),
		}); err != nil {
			return err
		}
		pos++
	}
	return nil
}

// servePlaylist writes a getPlaylist-style response for the given playlist.
func servePlaylist(ctx context.Context, h *Handler, c *reqCtx, p db.Playlist) {
	songs, _ := h.q.GetPlaylistSongs(ctx, p.ID)
	marks := h.loadMarks(ctx, c.userID)
	entries := make([]map[string]any, 0, len(songs))
	albums := h.albumsOf(ctx, songs)
	for _, s := range songs {
		alb := albums[s.AlbumID]
		sUUID := db.UUIDString(s.ID)
		entries = append(entries, buildSongChild(s, alb.Name, alb.ArtistName, alb.Year,
			marks.starredAt("song", sUUID), marks.ratingOf("song", sUUID)))
	}
	obj := playlistObj(p, int64(len(songs)), playlistDuration(songs), c.userID)
	obj["entry"] = entries
	c.ok(map[string]any{"playlist": obj})
}
