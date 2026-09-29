package subsonic

import (
	"net/http"
	"strings"
	"sync"

	"discodrive/internal/authlimit"
	"discodrive/internal/db"
	"discodrive/internal/secret"
	"discodrive/internal/storage"
)

// Handler is the Subsonic API HTTP handler. Mount it at /rest/.
type Handler struct {
	q           *db.Queries
	cipher      *secret.Cipher
	files       *storage.FileService // reserved for streaming/cover endpoints (later tasks)
	storageRoot string
	xaccel      bool

	scanMu    sync.Mutex
	scanState map[string]*scanInfo // keyed by userID

	authLimit *authlimit.Limiter // failed credential checks per client; nil = off
}

// scanInfo is the in-memory state of a user's library scan (single process).
type scanInfo struct {
	scanning bool
	count    int
}

// New creates a Handler. files and storageRoot may be nil/"" until streaming tasks are added.
func New(q *db.Queries, cipher *secret.Cipher, files *storage.FileService, storageRoot string, xaccel bool) *Handler {
	return &Handler{q: q, cipher: cipher, files: files, storageRoot: storageRoot, xaccel: xaccel, scanState: map[string]*scanInfo{}, authLimit: authlimit.NewDefault()}
}

// reqCtx carries everything an endpoint needs for a single Subsonic request.
type reqCtx struct {
	w      http.ResponseWriter
	r      *http.Request
	userID string
	format string // "json" or "xml"
}

// param returns a form/query parameter value.
func (c *reqCtx) param(name string) string { return c.r.FormValue(name) }

// paramList returns all values for a repeated form/query parameter (e.g. multiple songId).
func (c *reqCtx) paramList(name string) []string { return c.r.Form[name] }

// ok writes a successful Subsonic response with the given payload.
func (c *reqCtx) ok(payload any) { writeOK(c.w, c.format, payload) }

// fail writes a Subsonic error response.
func (c *reqCtx) fail(code int, msg string) { writeFail(c.w, c.format, code, msg) }

// endpoint is the function signature that every Subsonic endpoint handler must implement.
// Later tasks register additional endpoints by adding entries to the endpoints map inside
// their own init() functions:
//
//	func init() { endpoints["getArtists"] = getArtists }
type endpoint func(h *Handler, c *reqCtx)

// endpoints is the global dispatch map from Subsonic method name to handler.
// System endpoints are registered in system.go's init(); later tasks add browse/stream/etc.
var endpoints = map[string]endpoint{}

// ServeHTTP implements http.Handler. It parses the method from the URL path, authenticates
// the request, and dispatches to the registered endpoint.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Parse both query-string and POST-form body so FormValue works for all params.
	if err := r.ParseForm(); err != nil {
		writeFail(w, "xml", ErrGeneric, "bad request")
		return
	}

	// Determine response format: default is XML per Subsonic spec; "json" is explicit.
	format := r.FormValue("f")
	if format != "json" {
		format = "xml"
	}

	// Extract the method name from the path: strip leading "/rest/", trailing ".view".
	path := strings.TrimPrefix(r.URL.Path, "/rest/")
	path = strings.TrimSuffix(path, ".view")
	method := strings.Trim(path, "/")

	// Authenticate before dispatching. Only failed checks spend the per-client
	// budget; a blocked client gets no database lookup at all.
	if h.authLimit.Blocked(r) {
		writeFail(w, format, ErrGeneric, "Too many failed login attempts, try again later")
		return
	}
	userID, ok := h.authenticate(r)
	if !ok {
		if hasCredentials(r) {
			h.authLimit.Fail(r)
		}
		writeFail(w, format, ErrWrongAuth, "Wrong username or password")
		return
	}

	fn, found := endpoints[method]
	if !found {
		writeFail(w, format, ErrGeneric, "Unknown method")
		return
	}

	fn(h, &reqCtx{w: w, r: r, userID: userID, format: format})
}
