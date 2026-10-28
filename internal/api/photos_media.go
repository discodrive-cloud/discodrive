package api

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/auth"
	"discodrive/internal/db"
	"discodrive/internal/photos"
)

const photoCacheControl = "private, max-age=31536000, immutable"

// etagMatches reports whether an If-None-Match header names the etag.
func etagMatches(header, etag string) bool {
	for _, part := range strings.Split(header, ",") {
		p := strings.TrimPrefix(strings.TrimSpace(part), "W/")
		if p == "*" || p == etag {
			return true
		}
	}
	return false
}

// GET /me/photos/v1/assets/{id}/thumb and /preview — the derivative, built on first use.
// The client adds ?v=<thumb etag>, so the URL changes with the file and the response can
// be cached for good.
func (s *Server) photoDerivative(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.photosReady(w) {
			return
		}
		// The gallery trash shows thumbnails, so trashed cards are served here (only).
		a, ok := s.ownAssetState(w, r, true)
		if !ok {
			return
		}
		etag := `"` + photoThumbEtag(a) + `"`
		w.Header().Set("ETag", etag)
		w.Header().Set("Cache-Control", photoCacheControl)
		if a.ThumbState != "failed" && etagMatches(r.Header.Get("If-None-Match"), etag) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		file, err := s.thumbs.Ensure(r.Context(), a, kind)
		if err != nil {
			if r.Context().Err() != nil {
				return
			}
			if !errors.Is(err, photos.ErrRenderFailed) {
				log.Printf("discodrive: photos: %s of %s: %v", kind, db.UUIDString(a.ID), err)
			}
			w.Header().Del("ETag")
			w.Header().Del("Cache-Control")
			writeError(w, http.StatusNotFound, "render_failed")
			return
		}
		s.serveDerivative(w, r, file, "image/jpeg")
	}
}

func (s *Server) serveDerivative(w http.ResponseWriter, r *http.Request, file, contentType string) {
	f, err := os.Open(file)
	if err != nil {
		w.Header().Del("ETag")
		w.Header().Del("Cache-Control")
		writeError(w, http.StatusNotFound, "render_failed")
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, "", st.ModTime(), f)
}

// POST /me/photos/v1/assets/{id}/play {"hevc":bool} — how to play a video. A codec the
// client plays is streamed from the original; any other waits for the h264 copy, which
// the client polls for.
func (s *Server) handlePhotoPlay(w http.ResponseWriter, r *http.Request) {
	if !s.photosReady(w) {
		return
	}
	a, ok := s.ownAsset(w, r)
	if !ok {
		return
	}
	if a.Kind != "video" {
		writeError(w, http.StatusBadRequest, "not a video")
		return
	}
	var req struct {
		Hevc bool `json:"hevc"`
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
	}
	mint, err := s.auth.StreamMinter(r.Context(), auth.UserID(r.Context()))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	nodeID := db.UUIDString(a.NodeID)
	tok, err := mint(nodeID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	codec := strings.ToLower(a.VideoCodec.String)
	if codec == "h264" || (codec == "hevc" && req.Hevc) {
		writeJSON(w, http.StatusOK, map[string]any{"url": "/files/" + nodeID + "/stream?t=" + tok, "ready": true})
		return
	}
	unsupported := map[string]any{"url": nil, "ready": false, "unsupported": true}
	switch a.TranscodeState.String {
	case "ready":
		dst := photos.AssetDerivPath(s.storageRoot, a, "h264")
		if _, err := os.Stat(dst); err == nil {
			writeJSON(w, http.StatusOK, map[string]any{"url": "/me/photos/v1/assets/" + db.UUIDString(a.ID) + "/video?t=" + tok, "ready": true})
			return
		}
		// The copy is gone: queue it again.
		if err := s.q.SetTranscodeState(r.Context(), db.SetTranscodeStateParams{ID: a.ID, TranscodeState: pgtype.Text{}}); err != nil {
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
	case "failed":
		writeJSON(w, http.StatusOK, unsupported)
		return
	}
	if err := s.thumbs.QueueTranscode(r.Context(), a.ID); err != nil {
		if errors.Is(err, photos.ErrEconomy) {
			writeJSON(w, http.StatusOK, unsupported)
			return
		}
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"url": nil, "ready": false})
}

// GET /me/photos/v1/assets/{id}/video?t=<stream-v2 token of the asset's node> — the
// finished h264 copy, with ranges. Like /files/{id}/stream it is reachable by a <video>
// element, so the token (scoped to the node) is the credential.
func (s *Server) handlePhotoVideo(w http.ResponseWriter, r *http.Request) {
	if !s.photosReady(w) {
		return
	}
	id, err := db.ParseUUID(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	// The asset row tells which node the token has to be for.
	a, err := s.q.GetPhotoAssetByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	userID, err := s.auth.ValidateStreamToken(r.Context(), r.URL.Query().Get("t"), db.UUIDString(a.NodeID))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid stream token")
		return
	}
	if userID != db.UUIDString(a.UserID) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	// A hidden video is served too: its stream token was minted by play, which already
	// required the unlock, and a <video> element cannot send the X-Photos-Hidden header.
	if a.TrashedAt.Valid || a.Kind != "video" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	f, err := os.Open(photos.AssetDerivPath(s.storageRoot, a, "h264"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, no-store")
	http.ServeContent(w, r, "", st.ModTime(), f)
}
