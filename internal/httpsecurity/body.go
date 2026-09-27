package httpsecurity

import (
	"errors"
	"io"
	"net/http"
	"time"
)

// ReadDAVBody rejects oversized or incomplete objects before any DAV mutation.
// CalDAV/CardDAV objects are small; this is never used for WebDAV file uploads.
func ReadDAVBody(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, bool) {
	if r.ContentLength > limit {
		http.Error(w, "request body too large", 413)
		return nil, false
	}
	if r.Body == nil || r.Body == http.NoBody {
		return nil, true
	}
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Now().Add(30 * time.Second))
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	_ = r.Body.Close()
	_ = controller.SetReadDeadline(time.Time{})
	if err != nil {
		var sizeErr *http.MaxBytesError
		if errors.As(err, &sizeErr) {
			http.Error(w, "request body too large", 413)
		} else {
			http.Error(w, "could not read request body", 400)
		}
		return nil, false
	}
	return body, true
}
