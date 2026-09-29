package opds

import (
	"context"
	"net/http"
	"strings"

	"discodrive/internal/authlimit"
	"discodrive/internal/db"
	"discodrive/internal/secret"
)

// contextKey is an unexported type for context keys in this package.
type contextKey int

const ctxUserID contextKey = iota

// Handler is the OPDS catalog HTTP handler. Mount it at /opds and /opds/.
type Handler struct {
	q           *db.Queries
	cipher      *secret.Cipher
	storageRoot string
	xaccel      bool
	mux         *http.ServeMux
	authLimit   *authlimit.Limiter // failed credential checks per client; nil = off
}

// New creates a Handler and registers all OPDS route stubs.
func New(q *db.Queries, cipher *secret.Cipher, storageRoot string, xaccel bool) *Handler {
	h := &Handler{q: q, cipher: cipher, storageRoot: storageRoot, xaccel: xaccel, mux: http.NewServeMux(), authLimit: authlimit.NewDefault()}
	h.registerRoutes()
	return h
}

// registerRoutes adds handlers for all OPDS endpoints (nav feeds implemented in feeds.go;
// acquisition/download/search endpoints are stubs added by later tasks).
func (h *Handler) registerRoutes() {
	h.mux.HandleFunc("GET /opds", h.root)
	h.mux.HandleFunc("GET /opds/nav/authors", h.navAuthors)
	h.mux.HandleFunc("GET /opds/nav/series", h.navSeries)
	h.mux.HandleFunc("GET /opds/nav/genres", h.navGenres)
	h.mux.HandleFunc("GET /opds/new", h.acqNew)
	h.mux.HandleFunc("GET /opds/all", h.acqAll)
	h.mux.HandleFunc("GET /opds/author/{id}", h.acqAuthor)
	h.mux.HandleFunc("GET /opds/series/{id}", h.acqSeries)
	h.mux.HandleFunc("GET /opds/genre/{tag}", h.acqGenre)
	h.mux.HandleFunc("GET /opds/search.xml", h.searchDescription)
	h.mux.HandleFunc("GET /opds/search", h.search)
	h.mux.HandleFunc("GET /opds/download/{bookId}", h.download)
	h.mux.HandleFunc("GET /opds/cover/{bookId}", h.cover)
	h.mux.HandleFunc("GET /opds/cover/{bookId}/thumbnail", h.coverThumbnail)
}

// ServeHTTP authenticates the request and dispatches to the internal mux.
// On auth failure it writes 401 with a WWW-Authenticate header and returns.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Only failed checks spend the per-client budget; the anonymous first request
	// that asks for the Basic challenge does not.
	if h.authLimit.Blocked(r) {
		w.Header().Set("Retry-After", "60")
		http.Error(w, "Too many failed login attempts", http.StatusTooManyRequests)
		return
	}
	userID, ok := h.authenticate(r)
	if !ok {
		if r.FormValue("apiKey") != "" || r.Header.Get("Authorization") != "" {
			h.authLimit.Fail(r)
		}
		w.Header().Set("WWW-Authenticate", `Basic realm="discodrive OPDS"`)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	ctx := context.WithValue(r.Context(), ctxUserID, userID)
	h.mux.ServeHTTP(w, r.WithContext(ctx))
}

// userIDFromContext retrieves the authenticated userID stored by ServeHTTP.
func userIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(ctxUserID).(string)
	return v
}

// wantsJSON reports whether the client prefers application/opds+json over Atom/XML.
func wantsJSON(r *http.Request) bool {
	// Media-type matching is case-insensitive per RFC 7231 §3.1.1.1.
	return strings.Contains(strings.ToLower(r.Header.Get("Accept")), "application/opds+json")
}
