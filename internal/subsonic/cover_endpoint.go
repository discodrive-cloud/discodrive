package subsonic

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"discodrive/internal/coverthumb"
	"discodrive/internal/db"
	"discodrive/internal/music"
	"discodrive/internal/storage"
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

// maxCoverSource bounds a cover file read for thumbnailing; a larger one is served whole.
const maxCoverSource = 8 << 20

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
	if err != nil || !node.DiskPath.Valid || node.DeletedAt.Valid {
		http.Error(c.w, "no cover", http.StatusNotFound)
		return
	}
	// Access to the album does not imply access to its cover: the cover file can
	// sit in a folder the owner did not share (an album spread over folders).
	if node.UserID != userUUID {
		acc, err := h.q.SharedAccessForUser(ctx, db.SharedAccessForUserParams{StartID: node.ID, UserID: userUUID})
		if err != nil || !acc.CanRead {
			http.Error(c.w, "no cover", http.StatusNotFound)
			return
		}
	}

	// A size was asked for (album grids do): a cached thumbnail of one of the kept sizes,
	// made once per picture. Anything that cannot be thumbnailed falls through to the
	// full picture below, as before.
	if size, _ := strconv.Atoi(c.param("size")); coverthumb.Side(size) != 0 && node.ContentHash.Valid {
		side := coverthumb.Side(size)
		if music.CoverUnchanged(c.w, c.r, node.ContentHash.String+"-"+strconv.Itoa(side)) {
			return
		}
		thumb, ok := coverthumb.Get(h.storageRoot, node.ContentHash.String, side, func() ([]byte, bool) {
			if !isImageNode(node) {
				data, _, ok := music.EmbeddedStoredCover(h.storageRoot, filepath.Join(h.storageRoot, node.DiskPath.String))
				return data, ok
			}
			f, err := storage.NewLocalDisk(h.storageRoot).Open(node.DiskPath.String)
			if err != nil {
				return nil, false
			}
			defer f.Close()
			data, err := io.ReadAll(io.LimitReader(f, maxCoverSource))
			return data, err == nil
		})
		if ok {
			c.w.Header().Set("X-Content-Type-Options", "nosniff")
			c.w.Header().Set("Content-Type", "image/jpeg")
			http.ServeContent(c.w, c.r, "cover.jpg", time.Time{}, bytes.NewReader(thumb))
			return
		}
		c.w.Header().Del("ETag") // it named the thumbnail
		c.w.Header().Del("Cache-Control")
	}

	if isImageNode(node) {
		// Serve the image file directly (with Range support).
		h.serveNodeFile(c, node.DiskPath.String, node.Name, mimeForImage(node))
		return
	}

	// The node is an audio file with embedded cover art.
	if music.CoverUnchanged(c.w, c.r, node.ContentHash.String) {
		return
	}
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
