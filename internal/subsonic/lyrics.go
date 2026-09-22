package subsonic

import (
	"context"
	"path/filepath"
	"strings"

	"discodrive/internal/db"
	"discodrive/internal/music"
)

func init() {
	endpoints["getLyricsBySongId"] = getLyricsBySongId
	endpoints["getLyrics"] = getLyrics
}

// songFilePath resolves a track id to an absolute file path and song title.
// Returns (abs, title, true) on success; ("", "", false) on any failure.
func (h *Handler) songFilePath(ctx context.Context, userID, id string) (abs, title string, ok bool) {
	kind, songUUIDStr, parsed := decID(id)
	if !parsed || kind != "tr" {
		return "", "", false
	}

	userUUID, err := db.ParseUUID(userID)
	if err != nil {
		return "", "", false
	}

	songUUID, err := db.ParseUUID(songUUIDStr)
	if err != nil {
		return "", "", false
	}

	song, err := h.q.AccessibleSong(ctx, db.AccessibleSongParams{
		UserID: userUUID,
		ID:     songUUID,
	})
	if err != nil {
		return "", "", false
	}

	node, err := h.q.GetNode(ctx, song.NodeID)
	if err != nil || !node.DiskPath.Valid {
		return "", "", false
	}

	return filepath.Join(h.storageRoot, node.DiskPath.String), song.Title, true
}

// getLyricsBySongId implements the OpenSubsonic getLyricsBySongId endpoint.
// Param: id (kind "tr"). Returns structured lyrics or an empty lyricsList.
func getLyricsBySongId(h *Handler, c *reqCtx) {
	id := c.param("id")
	if id == "" {
		c.fail(ErrMissingParam, "Required parameter 'id' is missing")
		return
	}

	ctx := context.Background()
	absPath, title, ok := h.songFilePath(ctx, c.userID, id)
	if !ok {
		c.fail(ErrNotFound, "song not found")
		return
	}

	raw, _ := music.ReadStoredLyrics(h.storageRoot, absPath)
	if raw == "" {
		c.ok(map[string]any{"lyricsList": map[string]any{}})
		return
	}

	lines, isSynced := music.ParseLRC(raw)

	lineObjs := make([]map[string]any, 0, len(lines))
	for _, l := range lines {
		obj := map[string]any{"value": l.Text}
		if isSynced {
			obj["start"] = l.Start
		}
		lineObjs = append(lineObjs, obj)
	}

	c.ok(map[string]any{
		"lyricsList": map[string]any{
			"structuredLyrics": []map[string]any{{
				"displayArtist": "",
				"displayTitle":  title,
				"lang":          "xxx",
				"synced":        isSynced,
				"line":          lineObjs,
			}},
		},
	})
}

// getLyrics implements the legacy Subsonic getLyrics endpoint.
// Params: artist (ignored for lookup), title (used for DB lookup).
func getLyrics(h *Handler, c *reqCtx) {
	artist := c.param("artist")
	title := c.param("title")

	emptyResp := func() {
		c.ok(map[string]any{
			"lyrics": map[string]any{
				"artist": artist,
				"title":  title,
				"value":  "",
			},
		})
	}

	if title == "" {
		emptyResp()
		return
	}

	ctx := context.Background()
	userUUID, err := db.ParseUUID(c.userID)
	if err != nil {
		emptyResp()
		return
	}

	song, err := h.q.AccessibleSongByTitle(ctx, db.AccessibleSongByTitleParams{
		UserID: userUUID,
		Lower:  title, // the query lowercases both sides; pass the raw title
	})
	if err != nil {
		emptyResp()
		return
	}

	node, err := h.q.GetNode(ctx, song.NodeID)
	if err != nil || !node.DiskPath.Valid {
		emptyResp()
		return
	}

	absPath := filepath.Join(h.storageRoot, node.DiskPath.String)
	raw, _ := music.ReadStoredLyrics(h.storageRoot, absPath)
	if raw == "" {
		emptyResp()
		return
	}

	lines, _ := music.ParseLRC(raw)
	texts := make([]string, 0, len(lines))
	for _, l := range lines {
		texts = append(texts, l.Text)
	}
	value := strings.Join(texts, "\n")

	c.ok(map[string]any{
		"lyrics": map[string]any{
			"artist": artist,
			"title":  song.Title,
			"value":  value,
		},
	})
}
