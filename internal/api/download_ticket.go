package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"discodrive/internal/auth"
)

// ticketed registers a GET route a browser saves a file from: it takes the session
// header or a download ticket for its path (auth.MiddlewareOrTicket). The pattern is
// remembered so that tickets are minted for such routes only.
func (s *Server) ticketed(mux *http.ServeMux, pattern string, h http.Handler) {
	if s.ticketRoutes == nil {
		s.ticketRoutes = http.NewServeMux()
	}
	mux.Handle(pattern, s.auth.MiddlewareOrTicket(h))
	s.ticketRoutes.Handle(pattern, http.NotFoundHandler())
}

// POST /me/download-url {path} → {url}: a link the browser itself can download from,
// for a route registered with ticketed. Nothing is checked about the file here — the
// link runs the route's own checks as this user every time it is followed.
func (s *Server) handleDownloadURL(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if !s.takesTickets(req.Path) {
		writeError(w, http.StatusBadRequest, "not a download path")
		return
	}
	ticket, err := s.auth.DownloadTicket(r.Context(), req.Path)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": req.Path + "?" + auth.TicketParam + "=" + ticket})
}

func (s *Server) takesTickets(path string) bool {
	if s.ticketRoutes == nil || !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "?#%\\") || strings.Contains(path, "..") {
		return false
	}
	probe, err := http.NewRequest(http.MethodGet, path, nil)
	if err != nil || probe.URL.Path != path {
		return false
	}
	_, pattern := s.ticketRoutes.Handler(probe)
	return pattern != ""
}
