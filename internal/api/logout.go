package api

import (
	"discodrive/internal/auth"
	"errors"
	"net/http"
	"strings"
)

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	w.Header().Del("X-Token")
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || token == "" {
		writeError(w, http.StatusUnauthorized, "browser session required")
		return
	}
	if err := s.auth.Logout(r.Context(), token); err != nil {
		if errors.Is(err, auth.ErrInvalidCreds) {
			writeError(w, http.StatusUnauthorized, "browser session required")
		} else {
			writeError(w, 500, "could not revoke session")
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
