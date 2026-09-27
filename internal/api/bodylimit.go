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

// Match registered route patterns, not Content-Type or attacker-controlled path
// prefixes. File streams and DAV retain their own protocol-specific boundaries.
func limitAPIBodies(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, pattern := mux.Handler(r)
		switch pattern {
		case "", "/dav/", "/caldav/", "/carddav/", "/rest/", "/opds/", "/opds", "/users/", "/syncs/", "/app/",
			"POST /files/upload", "PUT /sync/file", "PUT /upload/{id}/chunk/{n}", "POST /me/contacts/import":
			mux.ServeHTTP(w, r)
			return
		}
		limit := int64(maxJSONBody)
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
