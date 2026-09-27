package api

import (
	"net/http"
	"net/url"
)

// streamableMime is the fixed allowlist of media types the stream endpoint serves.
// Anything else is refused outright (403), never downgraded to an attachment:
// this endpoint exists for <audio>/<video>, downloads go through /files/{id}/content.
// The allowlist is what makes Content-Disposition: inline safe here — an uploaded
// HTML/SVG can never come back executable on our origin through this route.
var streamableMime = map[string]bool{
	"audio/mpeg":       true,
	"audio/mp4":        true,
	"audio/mp4a-latm":  true, // what Go's mime.TypeByExtension returns for .m4a (see storage.detectMime)
	"audio/x-m4a":      true,
	"audio/aac":        true,
	"audio/flac":       true,
	"audio/x-flac":     true,
	"audio/ogg":        true,
	"audio/opus":       true,
	"audio/wav":        true,
	"audio/x-wav":      true,
	"audio/webm":       true,
	"video/mp4":        true,
	"video/webm":       true,
	"video/ogg":        true,
	"video/quicktime":  true,
	"video/x-matroska": true,
	"video/x-m4v":      true,
}

// setInlineMediaHeaders mirrors setDownloadHeaders but with inline disposition,
// which is only reachable for mimes from the streamableMime allowlist.
func setInlineMediaHeaders(w http.ResponseWriter, mime, name string) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Content-Disposition", "inline; filename*=UTF-8''"+url.PathEscape(name))
}

// GET /files/{id}/stream?t=<token> — media stream for <audio>/<video> elements,
// which cannot send an Authorization header. Auth = a purpose=stream JWT in the URL,
// scoped to exactly this node (minted by the media-listing endpoint). The token only
// identifies the user; node access is re-checked live on every request, so share
// revocation or access loss rejects subsequent requests (including new ranges).
// Bytes already being delivered by an authorized request cannot be recalled.
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	userID, err := s.auth.ValidateStreamToken(r.Context(), r.URL.Query().Get("t"), id)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid stream token")
		return
	}
	node, err := s.files.NodeForDownload(r.Context(), userID, id)
	if err != nil {
		writeStorageErr(w, err) // reject BEFORE X-Accel: access control is not delegated to nginx
		return
	}
	if !node.Mime.Valid || !streamableMime[node.Mime.String] {
		writeError(w, http.StatusForbidden, "not a streamable media type")
		return
	}
	if !s.xaccel {
		s.serveFileContent(w, r, node.Name, node.DiskPath.String, func() {
			setInlineMediaHeaders(w, node.Mime.String, node.Name)
		})
		return
	}
	setInlineMediaHeaders(w, node.Mime.String, node.Name)
	w.Header().Set("X-Accel-Redirect", xaccelRedirect(node.DiskPath.String))
}
