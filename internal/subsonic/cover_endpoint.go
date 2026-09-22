package subsonic

import (
	"bytes"
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"discodrive/internal/db"
	"discodrive/internal/music"
)

func init() {
	endpoints["getCoverArt"] = getCoverArt
}

// imageSuffixes is the set of file extensions we treat as image nodes.
var imageSuffixes = map[string]bool{
	"jpg": true, "jpeg": true, "png": true, "webp": true,
}

// isImageNode reports whether a node is a standalone image file (rather than an
// audio file with embedded art).
func isImageNode(node db.Node) bool {
	// Try the stored MIME type first.
	if node.Mime.Valid {
		m := strings.ToLower(node.Mime.String)
		if strings.HasPrefix(m, "image/") {
			return true
		}
	}
	// Fall back to the file extension.
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(node.Name), "."))
	return imageSuffixes[ext]
}

// mimeForImage returns a best-guess MIME type for an image node.
func mimeForImage(node db.Node) string {
	if node.Mime.Valid && node.Mime.String != "" {
		return node.Mime.String
	}
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(node.Name), "."))
	switch ext {
	case "png":
		return "image/png"
	case "webp":
		return "image/webp"
	default:
		return "image/jpeg"
	}
}

// mimeByExt returns a best-guess image MIME type from a file path's extension.
func mimeByExt(path string) string {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
	switch ext {
	case "png":
		return "image/png"
	case "webp":
		return "image/webp"
	default:
		return "image/jpeg"
	}
}

// getCoverArt implements the Subsonic getCoverArt endpoint.
//
// The id param is an encoded subsonic id: al-<uuid>, ar-<uuid>, or tr-<uuid>.
//   - al → look up the album's cover_art node.
//   - ar → look up the artist's cover_art node.
//   - tr → look up the song's album's cover_art node.
//
// Access is gated by the existing Accessible* scoped queries, so only albums/artists/
// songs the user can see are served.
func getCoverArt(h *Handler, c *reqCtx) {
	ctx := context.Background()
	userUUID, err := db.ParseUUID(c.userID)
	if err != nil {
		http.Error(c.w, "invalid user id", http.StatusBadRequest)
		return
	}

	kind, uuid, ok := decID(c.param("id"))
	if !ok {
		http.Error(c.w, "invalid id", http.StatusBadRequest)
		return
	}

	var coverNodeIDStr string

	switch kind {
	case "al":
		albumUUID, err := db.ParseUUID(uuid)
		if err != nil {
			http.Error(c.w, "invalid album id", http.StatusBadRequest)
			return
		}
		album, err := h.q.AccessibleAlbum(ctx, db.AccessibleAlbumParams{
			UserID: userUUID, ID: albumUUID,
		})
		if err != nil || !album.CoverArt.Valid || album.CoverArt.String == "" {
			http.Error(c.w, "no cover", http.StatusNotFound)
			return
		}
		coverNodeIDStr = album.CoverArt.String

	case "ar":
		artistUUID, err := db.ParseUUID(uuid)
		if err != nil {
			http.Error(c.w, "invalid artist id", http.StatusBadRequest)
			return
		}
		artist, err := h.q.AccessibleArtist(ctx, db.AccessibleArtistParams{
			UserID: userUUID, ID: artistUUID,
		})
		if err != nil || !artist.CoverArt.Valid || artist.CoverArt.String == "" {
			http.Error(c.w, "no cover", http.StatusNotFound)
			return
		}
		coverNodeIDStr = artist.CoverArt.String

	case "tr":
		songUUID, err := db.ParseUUID(uuid)
		if err != nil {
			http.Error(c.w, "invalid song id", http.StatusBadRequest)
			return
		}
		song, err := h.q.AccessibleSong(ctx, db.AccessibleSongParams{
			UserID: userUUID, ID: songUUID,
		})
		if err != nil {
			http.Error(c.w, "no cover", http.StatusNotFound)
			return
		}
		// Resolve the album's cover.
		albumRow, err := h.q.GetAlbumWithArtist(ctx, song.AlbumID)
		if err != nil || !albumRow.CoverArt.Valid || albumRow.CoverArt.String == "" {
			http.Error(c.w, "no cover", http.StatusNotFound)
			return
		}
		coverNodeIDStr = albumRow.CoverArt.String

	case "pc":
		channelUUID, err := db.ParseUUID(uuid)
		if err != nil {
			http.Error(c.w, "invalid podcast id", http.StatusBadRequest)
			return
		}
		channel, err := h.q.GetPodcastChannelForUser(ctx, db.GetPodcastChannelForUserParams{
			ID:     channelUUID,
			UserID: userUUID,
		})
		if err != nil || !channel.CoverPath.Valid || channel.CoverPath.String == "" {
			http.Error(c.w, "no cover", http.StatusNotFound)
			return
		}
		h.serveNodeFile(c, channel.CoverPath.String, "cover", mimeByExt(channel.CoverPath.String))
		return

	default:
		http.Error(c.w, "invalid id kind", http.StatusBadRequest)
		return
	}

	// Resolve the cover node UUID.
	coverNodeUUID, err := db.ParseUUID(coverNodeIDStr)
	if err != nil {
		http.Error(c.w, "no cover", http.StatusNotFound)
		return
	}

	node, err := h.q.GetNode(ctx, coverNodeUUID)
	if err != nil || !node.DiskPath.Valid {
		http.Error(c.w, "no cover", http.StatusNotFound)
		return
	}

	if isImageNode(node) {
		// Serve the image file directly (with Range support).
		h.serveNodeFile(c, node.DiskPath.String, node.Name, mimeForImage(node))
		return
	}

	// The node is an audio file with embedded cover art.
	absPath := filepath.Join(h.storageRoot, node.DiskPath.String)
	data, mimeType, ok2 := music.EmbeddedStoredCover(h.storageRoot, absPath)
	if !ok2 {
		http.Error(c.w, "no cover", http.StatusNotFound)
		return
	}

	// Serve with Range support via bytes.Reader.
	c.w.Header().Set("X-Content-Type-Options", "nosniff")
	c.w.Header().Set("Content-Type", mimeType)
	http.ServeContent(c.w, c.r, node.Name, time.Time{}, bytes.NewReader(data))
}
