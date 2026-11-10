package api

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

const maxJSONBody = 1 << 20
const maxAuthBody = 64 << 10

// bodyStall is how long a streamed body (file uploads, WebDAV) may send nothing before
// its read fails. A variable for tests.
var bodyStall = 2 * time.Minute

// Match registered route patterns, not Content-Type or attacker-controlled path
// prefixes. File streams and DAV retain their own protocol-specific boundaries.
func limitAPIBodies(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, pattern := mux.Handler(r)
		switch pattern {
		case "", "/dav/", "/dav", "/caldav/", "/carddav/", "/rest/", "/opds/", "/opds", "/users/", "/syncs/", "/app/",
			"POST /files/upload", "PUT /sync/file", "PUT /upload/{id}/chunk/{n}", "POST /me/contacts/import":
			if r.Body != nil && r.Body != http.NoBody {
				body := &stallBody{ReadCloser: r.Body, ctl: http.NewResponseController(w)}
				r.Body = body
				defer body.disarm()
			}
			mux.ServeHTTP(w, r)
			return
		}
		limit := int64(maxJSONBody)
		switch pattern {
		case "POST /me/bookmarks/bulk":
			limit = 64 << 20 // bounded bulk import, up to 20,000 records
		case "POST /me/saved":
			limit = 16 << 20 // 2 MiB HTML plus JSON escaping and metadata
		}
		if strings.Contains(pattern, " /auth/") || strings.Contains(pattern, " /setup/") || strings.Contains(pattern, " /pair/") || strings.Contains(pattern, " /me/identity/") || strings.Contains(pattern, " /me/webauthn/") || strings.Contains(pattern, " /me/totp") || pattern == "PUT /me/password" {
			limit = maxAuthBody
		}
		if r.ContentLength > limit {
			writeError(w, 413, "request body too large")
			return
		}
		if r.Body != nil && r.Body != http.NoBody {
			// Only small API bodies have this deadline; file streams/SSE are unaffected.
			controller := http.NewResponseController(w)
			_ = controller.SetReadDeadline(time.Now().Add(30 * time.Second))
			body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
			_ = r.Body.Close()
			_ = controller.SetReadDeadline(time.Time{})
			if err != nil {
				var sizeErr *http.MaxBytesError
				if errors.As(err, &sizeErr) {
					writeError(w, 413, "request body too large")
				} else {
					writeError(w, 400, "could not read request body")
				}
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		mux.ServeHTTP(w, r)
	})
}

// stallBody fails a streamed body's read once nothing has arrived for bodyStall. An
// upload holds an upload slot with its database connection while its body is read; the
// server has no read timeout for bodies (uploads may take hours), so without this a
// client that went silent kept the slot until the TCP connection died.
type stallBody struct {
	io.ReadCloser
	ctl   *http.ResponseController
	armed time.Time
}

func (b *stallBody) Read(p []byte) (int, error) {
	// Re-arm at most a few times per window, not on every 32 KiB read.
	if now := time.Now(); now.Sub(b.armed) > bodyStall/4 {
		b.armed = now
		_ = b.ctl.SetReadDeadline(now.Add(bodyStall))
	}
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		// The body is over: the deadline must not outlive it. After EOF net/http keeps
		// reading the connection to notice a disconnect, and a deadline firing there
		// would cancel the request while the handler is still publishing the file.
		b.disarm()
	}
	return n, err
}

func (b *stallBody) Close() error {
	b.disarm()
	return b.ReadCloser.Close()
}

func (b *stallBody) disarm() {
	if !b.armed.IsZero() {
		b.armed = time.Time{}
		_ = b.ctl.SetReadDeadline(time.Time{})
	}
}
