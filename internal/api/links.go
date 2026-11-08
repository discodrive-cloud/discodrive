package api

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"time"

	"discodrive/internal/auth"
	"discodrive/internal/db"
	"discodrive/internal/fetchguard"
	"discodrive/internal/links"
)

// linkCreatesPerMinute caps POST /me/links per user: far above anyone sharing by hand,
// low enough that a stolen token or a looping client cannot flood the fetch queue.
const linkCreatesPerMinute = 60

// maxLinkBody bounds the POST body: a URL and a title.
const maxLinkBody = 16 << 10

func newLinkLimiter() *rateLimiter {
	return &rateLimiter{counts: make(map[string]int), limit: linkCreatesPerMinute, window: time.Minute, windowStart: time.Now()}
}

// WithLinks turns on the "Share → DiscoDrive" inbox.
func WithLinks(l *links.Service) Option {
	return func(s *Server) {
		s.links = l
		s.linkLimiter = newLinkLimiter()
	}
}

func (s *Server) registerLinkRoutes(mux *http.ServeMux, prot func(http.Handler) http.Handler) {
	if s.links == nil {
		return
	}
	mux.Handle("POST /me/links", prot(http.HandlerFunc(s.handleLinkCreate)))
	mux.Handle("GET /me/links", prot(http.HandlerFunc(s.handleLinksList)))
	mux.Handle("PATCH /me/links/{id}", prot(http.HandlerFunc(s.handleLinkUpdate)))
	mux.Handle("DELETE /me/links/{id}", prot(http.HandlerFunc(s.handleLinkDelete)))
	mux.Handle("GET /me/links/{id}/thumb", prot(http.HandlerFunc(s.handleLinkThumb)))
}

// POST /me/links {url, title?} — save a shared link (or bring it back to the top).
// 400 means the link will never be accepted, so clients drop it instead of retrying.
func (s *Server) handleLinkCreate(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserID(r.Context())
	uid, err := db.ParseUUID(userID)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid token subject")
		return
	}
	if !s.linkLimiter.allow(userID) {
		writeError(w, http.StatusTooManyRequests, "too many links, please try again later")
		return
	}
	var req struct {
		URL   string `json:"url"`
		Title string `json:"title"`
		// When the phone shared it, for a link that waited in its outbox (RFC 3339).
		SharedAt *time.Time `json:"shared_at"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxLinkBody)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	sharedAt := time.Now()
	if req.SharedAt != nil {
		sharedAt = *req.SharedAt
	}
	l, err := s.links.SaveShared(r.Context(), uid, req.URL, req.Title, sharedAt)
	switch {
	case errors.Is(err, links.ErrInvalidURL):
		writeError(w, http.StatusBadRequest, "url must be an http or https link")
		return
	case errors.Is(err, fetchguard.ErrBlocked):
		// The guard's error names the resolved addresses: log the fact, not the link.
		log.Printf("discodrive: links: a link was refused by the SSRF guard")
		writeError(w, http.StatusBadRequest, "url is not allowed")
		return
	case errors.Is(err, links.ErrTooMany):
		writeError(w, http.StatusInsufficientStorage, "the link inbox is full: delete some links first")
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, l)
}

// GET /me/links?archived=0|1&source= — inbox or archive, newest first.
func (s *Server) handleLinksList(w http.ResponseWriter, r *http.Request) {
	uid, err := db.ParseUUID(auth.UserID(r.Context()))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid token subject")
		return
	}
	archived := false
	if v := r.URL.Query().Get("archived"); v != "" {
		if archived, err = strconv.ParseBool(v); err != nil {
			writeError(w, http.StatusBadRequest, "invalid archived flag")
			return
		}
	}
	p, err := s.links.List(r.Context(), uid, archived, r.URL.Query().Get("source"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// PATCH /me/links/{id} {archived} — move to the archive or back to the inbox.
func (s *Server) handleLinkUpdate(w http.ResponseWriter, r *http.Request) {
	uid, err := db.ParseUUID(auth.UserID(r.Context()))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid token subject")
		return
	}
	var req struct {
		Archived *bool `json:"archived"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxLinkBody)).Decode(&req); err != nil || req.Archived == nil {
		writeError(w, http.StatusBadRequest, "archived is required")
		return
	}
	s.linkResult(w, s.links.SetArchived(r.Context(), uid, r.PathValue("id"), *req.Archived))
}

// DELETE /me/links/{id}
func (s *Server) handleLinkDelete(w http.ResponseWriter, r *http.Request) {
	uid, err := db.ParseUUID(auth.UserID(r.Context()))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid token subject")
		return
	}
	s.linkResult(w, s.links.Delete(r.Context(), uid, r.PathValue("id")))
}

func (s *Server) linkResult(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, links.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	case err != nil:
		writeError(w, http.StatusInternalServerError, "internal error")
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// GET /me/links/{id}/thumb — the preview the server made. It is always a JPEG the server
// encoded itself, served with a pinned type and a sandbox, so even a bug upstream of it
// cannot turn this response into a page.
func (s *Server) handleLinkThumb(w http.ResponseWriter, r *http.Request) {
	uid, err := db.ParseUUID(auth.UserID(r.Context()))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid token subject")
		return
	}
	b, err := s.links.Thumb(r.Context(), uid, r.PathValue("id"))
	if errors.Is(err, links.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	h := w.Header()
	h.Set("Content-Type", "image/jpeg")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	h.Set("Cache-Control", "private, max-age=86400")
	h.Set("Content-Length", strconv.Itoa(len(b)))
	_, _ = w.Write(b)
}
