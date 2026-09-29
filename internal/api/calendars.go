package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"discodrive/internal/auth"
	"discodrive/internal/dav"
	"discodrive/internal/db"
)

type calendarDTO struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Color      string `json:"color"`
	IsDefault  bool   `json:"is_default"`
	IsOwner    bool   `json:"is_owner"`
	OwnerEmail string `json:"owner_email,omitempty"`
	// ShareID — for a calendar shared with the user: DELETE /me/calendars/{id}/shares/{share_id}
	// leaves it.
	ShareID string `json:"share_id,omitempty"`
}

// GET /me/calendars — VEVENT-capable calendars for the current user.
func (s *Server) handleListCalendars(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserID(r.Context())
	// ensure new users have a default calendar (created lazily)
	if _, err := s.dav.EnsureDefaultCalendar(r.Context(), userID); err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	cals, err := s.dav.ListCalendars(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := make([]calendarDTO, 0, len(cals))
	defaultSet := false
	for _, c := range cals {
		if !strings.Contains(c.Components, "VEVENT") {
			continue
		}
		dto := calendarDTO{ID: db.UUIDString(c.ID), Name: c.Name, Color: c.Color, IsOwner: true}
		if !defaultSet {
			dto.IsDefault = true
			defaultSet = true
		}
		out = append(out, dto)
	}
	if shared, serr := s.dav.SharedCalendarsForUser(r.Context(), userID); serr == nil {
		for _, sc := range shared {
			if !strings.Contains(sc.Calendar.Components, "VEVENT") {
				continue
			}
			out = append(out, calendarDTO{
				ID:         db.UUIDString(sc.Calendar.ID),
				Name:       sc.Calendar.Name,
				Color:      sc.Calendar.Color,
				IsOwner:    false,
				OwnerEmail: sc.OwnerEmail,
				ShareID:    sc.ShareID,
			})
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// POST /me/calendars {name, color}
func (s *Server) handleCreateCalendar(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name  string `json:"name"`
		Color string `json:"color"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	c, err := s.dav.CreateCalendar(r.Context(), auth.UserID(r.Context()), body.Name, body.Color)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": db.UUIDString(c.ID)})
}

// PATCH /me/calendars/{id} {name?, color?}
func (s *Server) handleUpdateCalendar(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserID(r.Context())
	calID := r.PathValue("id")
	c, err := s.dav.GetCalendar(r.Context(), calID)
	if err == dav.ErrNotFound || (err == nil && db.UUIDString(c.UserID) != userID) {
		writeError(w, http.StatusNotFound, "calendar not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	var body struct {
		Name  *string `json:"name"`
		Color *string `json:"color"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if body.Name != nil && strings.TrimSpace(*body.Name) != "" {
		if err := s.dav.SetCalendarName(r.Context(), userID, calID, *body.Name); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to save")
			return
		}
	}
	if body.Color != nil {
		if err := s.dav.SetCalendarColor(r.Context(), userID, calID, *body.Color); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to save")
			return
		}
	}
	w.WriteHeader(http.StatusOK)
}

// DELETE /me/calendars/{id}
func (s *Server) handleDeleteCalendar(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserID(r.Context())
	switch err := s.dav.DeleteCalendar(r.Context(), userID, r.PathValue("id")); {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, dav.ErrBadID):
		writeError(w, http.StatusBadRequest, "invalid calendar id")
	case errors.Is(err, dav.ErrNotFound):
		writeError(w, http.StatusNotFound, "calendar not found")
	default:
		writeError(w, http.StatusInternalServerError, "failed to delete")
	}
}
