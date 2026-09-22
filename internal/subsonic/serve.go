package subsonic

import (
	"net/http"
	"net/url"

	"discodrive/internal/safecontent"
	"discodrive/internal/storage"
)

// serveNodeFile streams a node's file with Range support.
// Mirrors internal/api streamFile / xaccelRedirect.
//
// diskPath is the relative path stored in nodes.disk_path.
// name is used by http.ServeContent for the filename hint (content-disposition etc.).
// contentType, if non-empty, is set on the response before serving.
//
// In direct mode (!xaccel) the file is opened from storageRoot and served via
// http.ServeContent, which handles Range, If-Modified-Since, and Etag automatically.
//
// In xaccel mode an X-Accel-Redirect header is sent and nginx serves the bytes;
// the path is URL-escaped (spaces/unicode → %XX, slashes preserved).
func (h *Handler) serveNodeFile(c *reqCtx, diskPath, name, contentType string) {
	ct, ok := safecontent.InlineType(contentType)
	if !ok {
		http.Error(c.w, "unsupported media type", http.StatusForbidden)
		return
	}
	c.w.Header().Set("X-Content-Type-Options", "nosniff")
	c.w.Header().Set("Content-Type", ct)

	if !h.xaccel {
		f, err := storage.NewLocalDisk(h.storageRoot).Open(diskPath)
		if err != nil {
			http.Error(c.w, "not found", http.StatusNotFound)
			return
		}
		defer f.Close()
		fi, err := f.Stat()
		if err != nil {
			http.Error(c.w, "internal error", http.StatusInternalServerError)
			return
		}
		http.ServeContent(c.w, c.r, name, fi.ModTime(), f)
		return
	}

	// nginx mode: set X-Accel-Redirect and let nginx deliver the bytes.
	u := url.URL{Path: "/__data/" + diskPath}
	c.w.Header().Set("X-Accel-Redirect", u.EscapedPath())
}
