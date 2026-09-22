package opds

import (
	"net/http"
	"net/url"

	"discodrive/internal/storage"
)

// serveNodeFile streams a node's file with Range support.
// Ported from internal/subsonic serveNodeFile — adapted for the opds Handler
// (uses http.ResponseWriter + *http.Request directly instead of *reqCtx).
//
// diskPath is the relative path stored in nodes.disk_path.
// name is used by http.ServeContent for the filename hint.
// contentType, if non-empty, is set on the response before serving.
//
// In direct mode (!xaccel) the file is opened from storageRoot and served via
// http.ServeContent, which handles Range, If-Modified-Since, and Etag automatically.
//
// In xaccel mode an X-Accel-Redirect header is sent and nginx serves the bytes;
// the path is URL-escaped (spaces/unicode → %XX, slashes preserved).
func (h *Handler) serveNodeFile(w http.ResponseWriter, r *http.Request, diskPath, name, contentType string) {
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}

	if !h.xaccel {
		f, err := storage.NewLocalDisk(h.storageRoot).Open(diskPath)
		if err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		defer f.Close()
		fi, err := f.Stat()
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		http.ServeContent(w, r, name, fi.ModTime(), f)
		return
	}

	// nginx mode: set X-Accel-Redirect and let nginx deliver the bytes.
	u := url.URL{Path: "/__data/" + diskPath}
	w.Header().Set("X-Accel-Redirect", u.EscapedPath())
}
