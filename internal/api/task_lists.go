package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"

	"discodrive/internal/auth"
	"discodrive/internal/dav"
	"discodrive/internal/db"
)

// Task lists are the user's VTODO collections — the same lists Apple Reminders shows.
// The default one is the earliest created (EnsureDefaultTaskList), as before lists existed.

type taskListDTO struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Color     string `json:"color"`
	IsDefault bool   `json:"is_default"`
}

var errNoTaskList = errors.New("task list not found")

// taskLists returns the user's VTODO collections in display order (Reminders' calendar-order,
// then creation); the first element of the second result is the default list.
func (s *Server) taskLists(r *http.Request) ([]db.Calendar, db.Calendar, error) {
	userID := auth.UserID(r.Context())
	def, err := s.dav.EnsureDefaultTaskList(r.Context(), userID)
	if err != nil {
		return nil, db.Calendar{}, err
	}
	cals, err := s.dav.ListCalendars(r.Context(), userID)
	if err != nil {
		return nil, db.Calendar{}, err
	}
	out := make([]db.Calendar, 0, len(cals))
	for _, c := range cals {
		if strings.Contains(c.Components, "VTODO") {
			out = append(out, c)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i].SortOrder, out[j].SortOrder
		if a.Valid != b.Valid {
			return a.Valid
		}
		return a.Valid && a.Int32 < b.Int32
	})
	return out, def, nil
}

// taskList resolves a list id of the current user ("" = the default list).
func (s *Server) taskList(r *http.Request, id string) (db.Calendar, error) {
	lists, def, err := s.taskLists(r)
	if err != nil {
		return db.Calendar{}, err
	}
	if id == "" {
		return def, nil
	}
	for _, c := range lists {
		if db.UUIDString(c.ID) == id {
			return c, nil
		}
	}
	return db.Calendar{}, errNoTaskList
}

// GET /me/task-lists
func (s *Server) handleListTaskLists(w http.ResponseWriter, r *http.Request) {
	lists, def, err := s.taskLists(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := make([]taskListDTO, 0, len(lists))
	for _, c := range lists {
		out = append(out, taskListDTO{ID: db.UUIDString(c.ID), Name: c.Name, Color: c.Color, IsDefault: c.ID == def.ID})
	}
	writeJSON(w, http.StatusOK, out)
}

// POST /me/task-lists {name, color}
func (s *Server) handleCreateTaskList(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name  string `json:"name"`
		Color string `json:"color"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	// make sure the default list exists first, so a new list never becomes the default
	if _, err := s.dav.EnsureDefaultTaskList(r.Context(), auth.UserID(r.Context())); err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	c, err := s.dav.CreateTaskList(r.Context(), auth.UserID(r.Context()), name, body.Color)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create")
		return
	}
	writeJSON(w, http.StatusCreated, taskListDTO{ID: db.UUIDString(c.ID), Name: c.Name, Color: c.Color})
}

// PATCH /me/task-lists/{id} {name?, color?}
func (s *Server) handleUpdateTaskList(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserID(r.Context())
	c, err := s.taskList(r, r.PathValue("id"))
	if errors.Is(err, errNoTaskList) {
		writeError(w, http.StatusNotFound, "task list not found")
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
	id := db.UUIDString(c.ID)
	if body.Name != nil && strings.TrimSpace(*body.Name) != "" {
		if err := s.dav.SetCalendarName(r.Context(), userID, id, strings.TrimSpace(*body.Name)); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to save")
			return
		}
	}
	if body.Color != nil {
		if err := s.dav.SetCalendarColor(r.Context(), userID, id, *body.Color); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to save")
			return
		}
	}
	w.WriteHeader(http.StatusOK)
}

// DELETE /me/task-lists/{id} — deletes the list with its tasks; the last list stays.
func (s *Server) handleDeleteTaskList(w http.ResponseWriter, r *http.Request) {
	lists, _, err := s.taskLists(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	id := r.PathValue("id")
	found := false
	for _, c := range lists {
		if db.UUIDString(c.ID) == id {
			found = true
		}
	}
	if !found {
		writeError(w, http.StatusNotFound, "task list not found")
		return
	}
	if len(lists) < 2 {
		writeError(w, http.StatusConflict, "the last task list cannot be deleted")
		return
	}
	switch err := s.dav.DeleteCalendar(r.Context(), auth.UserID(r.Context()), id); {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, dav.ErrNotFound):
		writeError(w, http.StatusNotFound, "task list not found")
	default:
		writeError(w, http.StatusInternalServerError, "failed to delete")
	}
}
