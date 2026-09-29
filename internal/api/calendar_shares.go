package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"discodrive/internal/auth"
	"discodrive/internal/dav"
	"discodrive/internal/db"
)

// POST /me/calendars/{id}/share {email, expires_in_seconds?}
func (s *Server) handleShareCalendar(w http.ResponseWriter, r *http.Request) {
	owner := auth.UserID(r.Context())
	calID := r.PathValue("id")
	var body struct {
		Email            string `json:"email"`
		ExpiresInSeconds int64  `json:"expires_in_seconds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	var exp *time.Time
	if body.ExpiresInSeconds > 0 {
		t := time.Now().Add(time.Duration(body.ExpiresInSeconds) * time.Second)
		exp = &t
	}
	share, err := s.dav.ShareCalendar(r.Context(), owner, calID, body.Email, exp)
	if !writeShareResult(w, err, "calendar not found") {
		return
	}
	// notify the recipient (best-effort, same as for file shares)
	calName := ""
	if c, e := s.dav.GetCalendar(r.Context(), calID); e == nil {
		calName = c.Name
	}
	sharerEmail := ""
	if su, e := s.q.GetUserByID(r.Context(), mustUUID(owner)); e == nil {
		sharerEmail = su.Email
	}
	s.notify.Emit(r.Context(), db.UUIDString(share.SharedWithUser), "share.received",
		map[string]any{"NodeName": calName, "SharerEmail": sharerEmail, "ResourceLabel": "calendar"})
	writeShareOK(w)
}

// writeShareResult answers a failed share of a collection and reports whether the caller
// should go on (a share was made). An unknown recipient is answered exactly like a success
// (see writeShareOK) so that the endpoint cannot be used to probe which emails have an
// account. notFound is the message for a missing collection.
func writeShareResult(w http.ResponseWriter, err error, notFound string) bool {
	switch {
	case err == nil:
		return true
	case errors.Is(err, dav.ErrNotOwner):
		writeError(w, http.StatusForbidden, "only the owner can share")
	case errors.Is(err, dav.ErrSelfShare):
		writeError(w, http.StatusBadRequest, "cannot share with yourself")
	case errors.Is(err, dav.ErrRecipientNotFound):
		writeShareOK(w)
	case errors.Is(err, dav.ErrNotFound):
		writeError(w, http.StatusNotFound, notFound)
	default:
		writeError(w, http.StatusInternalServerError, "failed to share")
	}
	return false
}

func writeShareOK(w http.ResponseWriter) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

// GET /me/calendars/{id}/shares
func (s *Server) handleListCalendarShares(w http.ResponseWriter, r *http.Request) {
	infos, err := s.dav.ListCalendarShares(r.Context(), auth.UserID(r.Context()), r.PathValue("id"))
	switch err {
	case nil:
	case dav.ErrNotOwner:
		writeError(w, http.StatusForbidden, "owner only")
		return
	case dav.ErrNotFound:
		writeError(w, http.StatusNotFound, "calendar not found")
		return
	default:
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	type shareDTO struct {
		ID        string `json:"id"`
		Email     string `json:"email"`
		ExpiresAt string `json:"expires_at,omitempty"`
	}
	out := make([]shareDTO, 0, len(infos))
	for _, i := range infos {
		out = append(out, shareDTO{ID: i.ID, Email: i.Email, ExpiresAt: i.ExpiresAt})
	}
	writeJSON(w, http.StatusOK, out)
}

// DELETE /me/calendars/{id}/shares/{shareId} — the owner revokes a share, or the recipient
// leaves the calendar (shareId: share_id of GET /me/calendars).
func (s *Server) handleDeleteCalendarShare(w http.ResponseWriter, r *http.Request) {
	err := s.dav.DeleteCalendarShare(r.Context(), auth.UserID(r.Context()), r.PathValue("shareId"))
	switch err {
	case nil:
		w.WriteHeader(http.StatusNoContent)
	case dav.ErrNotOwner:
		writeError(w, http.StatusForbidden, "owner only")
	case dav.ErrNotFound:
		writeError(w, http.StatusNotFound, "share not found")
	default:
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}
