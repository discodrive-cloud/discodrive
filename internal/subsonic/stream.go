package subsonic

import (
	"context"
	"errors"
	"log"
	"syscall"

	"discodrive/internal/db"
	"discodrive/internal/safecontent"
)

func init() {
	endpoints["stream"] = stream
	endpoints["download"] = download
}

// resolveAndServeSong resolves and authorizes a track id from the request, then
// streams the underlying file. Used by both stream and download.
//
// Accepted params:
//
//	id  — required; must decode to kind "tr".
//
// The params maxBitRate and format are accepted but ignored: direct-play only,
// no transcoding is performed.
func (h *Handler) resolveAndServeSong(c *reqCtx) {
	id := c.param("id")
	if id == "" {
		c.fail(ErrMissingParam, "Required parameter 'id' is missing")
		return
	}

	kind, uuidStr, ok := decID(id)
	if !ok {
		c.fail(ErrNotFound, "Song not found")
		return
	}

	userUUID, err := db.ParseUUID(c.userID)
	if err != nil {
		c.fail(ErrGeneric, "invalid user id")
		return
	}

	ctx := context.Background()

	switch kind {
	case "tr":
		songUUID, err := db.ParseUUID(uuidStr)
		if err != nil {
			c.fail(ErrNotFound, "Song not found")
			return
		}

		song, err := h.q.AccessibleSong(ctx, db.AccessibleSongParams{
			UserID: userUUID,
			ID:     songUUID,
		})
		if err != nil {
			c.fail(ErrNotFound, "Song not found")
			return
		}

		node, err := h.q.GetNode(ctx, song.NodeID)
		if err != nil {
			c.fail(ErrNotFound, "Song not found")
			return
		}

		if !node.DiskPath.Valid {
			c.fail(ErrNotFound, "Song file not found")
			return
		}

		contentType := ""
		if song.ContentType.Valid {
			contentType = song.ContentType.String
		}

		h.serveNodeFile(c, node.DiskPath.String, node.Name, contentType)

	case "pe":
		epUUID, err := db.ParseUUID(uuidStr)
		if err != nil {
			c.fail(ErrNotFound, "episode not found")
			return
		}

		ep, err := h.q.GetEpisodeForUser(ctx, db.GetEpisodeForUserParams{
			ID:     epUUID,
			UserID: userUUID,
		})
		if err != nil {
			c.fail(ErrNotFound, "episode not found")
			return
		}

		if ep.Status == "completed" && ep.DiskPath.Valid {
			// The recorded type is whatever the host sent (audio/mp3, octet-stream,
			// ...); serve the allowlisted type it or the file extension implies.
			ct, _ := safecontent.MediaFor(ep.ContentType, ep.DiskPath.String)
			h.serveNodeFile(c, ep.DiskPath.String, ep.Title, ct)
			return
		}

		// Not downloaded yet: proxy the original audio URL so clients still get
		// real audio (instead of a Subsonic error envelope they'd try to decode).
		// ProxyStream writes the body directly; do not also write an ok envelope.
		committed, err := proxyEpisode(ctx, c.w, c.r, ep.AudioUrl)
		if err != nil && !committed {
			// Nothing was written to the client yet — safe to emit an error.
			c.fail(ErrNotFound, "episode unavailable")
			return
		}
		// If committed, the partial stream already went to the client; writing an
		// error envelope now would corrupt the audio bytes. Log only genuine
		// failures — a client closing the connection (seek/skip/stop) surfaces as
		// broken pipe / connection reset / canceled context and is normal.
		if err != nil && !isClientDisconnect(err) {
			log.Printf("discodrive: stream episode %s: proxy failed after partial stream: %v", uuidStr, err)
		}

	default:
		c.fail(ErrNotFound, "Song not found")
	}
}

// isClientDisconnect reports whether err is a normal client-side disconnect
// during streaming (seek/skip/stop): broken pipe, connection reset, or a
// canceled request context. These are expected and not worth logging.
func isClientDisconnect(err error) bool {
	return errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, context.Canceled)
}

// stream implements the Subsonic stream endpoint.
// It serves the original audio file with Range support (no transcoding).
func stream(h *Handler, c *reqCtx) {
	h.resolveAndServeSong(c)
}

// download implements the Subsonic download endpoint.
// Semantically identical to stream (no play-count tracking here).
func download(h *Handler, c *reqCtx) {
	h.resolveAndServeSong(c)
}
