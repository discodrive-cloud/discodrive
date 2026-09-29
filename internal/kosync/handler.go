// Package kosync implements the KOReader kosync reading-progress sync protocol.
// It exposes four endpoints that KOReader clients use to push and pull per-document
// reading positions, authenticated via x-auth-user (email) + x-auth-key (md5 of ebook password).
package kosync

import (
	"encoding/json"
	"net/http"

	"discodrive/internal/authlimit"
	"discodrive/internal/db"
	"discodrive/internal/secret"
)

// Handler is the kosync HTTP handler. Mount it at /users/ and /syncs/.
type Handler struct {
	q      *db.Queries
	cipher *secret.Cipher
	mux    *http.ServeMux

	authLimit *authlimit.Limiter // failed credential checks per client; nil = off
}

// New creates a Handler and registers all kosync routes.
func New(q *db.Queries, cipher *secret.Cipher) *Handler {
	h := &Handler{q: q, cipher: cipher, mux: http.NewServeMux(), authLimit: authlimit.NewDefault()}
	h.registerRoutes()
	return h
}

// registerRoutes wires up the four kosync endpoints.
func (h *Handler) registerRoutes() {
	h.mux.HandleFunc("GET /users/auth", h.usersAuth)
	h.mux.HandleFunc("POST /users/create", h.usersCreate)
	h.mux.HandleFunc("PUT /syncs/progress", h.syncsProgressPut)
	h.mux.HandleFunc("GET /syncs/progress/{document}", h.syncsProgressGet)
}

// ServeHTTP dispatches to the internal mux. Auth is handled per-endpoint.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.authLimit.Blocked(r) {
		w.Header().Set("Retry-After", "60")
		writeJSON(w, http.StatusTooManyRequests, struct{}{})
		return
	}
	h.mux.ServeHTTP(w, r)
}

// writeJSON sets Content-Type and encodes v as JSON.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
