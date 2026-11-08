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
	ID         string `json:"id"`
	Name       string `json:"name"`
	Color      string `json:"color"`
	IsDefault  bool   `json:"is_default"`
	IsOwner    bool   `json:"is_owner"`
	OwnerEmail string `json:"owner_email,omitempty"` // a list shared with the user: whose it is
	ShareID    string `json:"share_id,omitempty"`    // … and the share to leave it with
}

var (
	errNoTaskList   = errors.New("task list not found")
	errNotListOwner = errors.New("only the owner can change the task list")
)

// sharedTaskList is a VTODO collection someone shared with the user.
type sharedTaskList struct {
	cal        db.Calendar
	ownerEmail string
	shareID    string
}

// sharedTaskLists returns the task lists shared with the current user.
func (s *Server) sharedTaskLists(r *http.Request) ([]sharedTaskList, error) {
	shared, err := s.dav.SharedCalendarsForUser(r.Context(), auth.UserID(r.Context()))
	if err != nil {
		return nil, err
	}
	out := make([]sharedTaskList, 0)
	for _, sc := range shared {
		if strings.Contains(sc.Calendar.Components, "VTODO") {
			out = append(out, sharedTaskList{cal: sc.Calendar, ownerEmail: sc.OwnerEmail, shareID: sc.ShareID})
		}
	}
	return out, nil
}

// ownTaskList resolves one of the user's own lists (not a shared one): errNotListOwner for a
// list shared with the user, errNoTaskList for anything else.
func (s *Server) ownTaskList(r *http.Request, id string) (db.Calendar, error) {
	lists, _, err := s.taskLists(r)
	if err != nil {
		return db.Calendar{}, err
	}
	for _, c := range lists {
		if db.UUIDString(c.ID) == id {
			return c, nil
		}
	}
	shared, err := s.sharedTaskLists(r)
	if err != nil {
		return db.Calendar{}, err
	}
	for _, sl := range shared {
		if db.UUIDString(sl.cal.ID) == id {
			return db.Calendar{}, errNotListOwner
		}
	}
	return db.Calendar{}, errNoTaskList
}

// accessibleTaskLists is the user's own lists followed by the ones shared with them.
func (s *Server) accessibleTaskLists(r *http.Request) ([]db.Calendar, error) {
	lists, _, err := s.taskLists(r)
	if err != nil {
		return nil, err
	}
	shared, err := s.sharedTaskLists(r)
	if err != nil {
		return nil, err
	}
	for _, sl := range shared {
		lists = append(lists, sl.cal)
	}
	return lists, nil
}

// taskLists returns the user's own VTODO collections in display order (Reminders'
// calendar-order, then creation); the second result is the default list.
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

// taskList resolves a list the current user may work with — own or shared with them
// ("" = the default list).
func (s *Server) taskList(r *http.Request, id string) (db.Calendar, error) {
	if id == "" {
		_, def, err := s.taskLists(r)
		return def, err
	}
	lists, err := s.accessibleTaskLists(r)
	if err != nil {
		return db.Calendar{}, err
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
	shared, err := s.sharedTaskLists(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := make([]taskListDTO, 0, len(lists)+len(shared))
	for _, c := range lists {
		out = append(out, taskListDTO{ID: db.UUIDString(c.ID), Name: c.Name, Color: c.Color, IsDefault: c.ID == def.ID, IsOwner: true})
	}
	for _, sl := range shared {
		out = append(out, taskListDTO{ID: db.UUIDString(sl.cal.ID), Name: sl.cal.Name, Color: sl.cal.Color,
			OwnerEmail: sl.ownerEmail, ShareID: sl.shareID})
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
	writeJSON(w, http.StatusCreated, taskListDTO{ID: db.UUIDString(c.ID), Name: c.Name, Color: c.Color, IsOwner: true})
}

// PATCH /me/task-lists/{id} {name?, color?}
func (s *Server) handleUpdateTaskList(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserID(r.Context())
	c, err := s.ownTaskList(r, r.PathValue("id"))
	if err != nil {
		writeTaskListError(w, err)
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
	id := r.PathValue("id")
	if _, err := s.ownTaskList(r, id); err != nil {
		writeTaskListError(w, err)
		return
	}
	lists, _, err := s.taskLists(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
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

// writeTaskListError answers an ownTaskList failure.
func writeTaskListError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errNoTaskList):
		writeError(w, http.StatusNotFound, "task list not found")
	case errors.Is(err, errNotListOwner):
		writeError(w, http.StatusForbidden, "only the owner can change the task list")
	default:
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}

// POST /me/task-lists/{id}/share {email} — gives one more user full access to the list
// (again for the same user is a no-op). Each recipient is a separate share.
func (s *Server) handleShareTaskList(w http.ResponseWriter, r *http.Request) {
	if !s.shareAllowed(w, r) {
		return
	}
	c, err := s.ownTaskList(r, r.PathValue("id"))
	if err != nil {
		writeTaskListError(w, err)
		return
	}
	var body struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	email := strings.TrimSpace(body.Email)
	if email == "" {
		writeError(w, http.StatusBadRequest, "email is required")
		return
	}
	owner := auth.UserID(r.Context())
	share, fresh, err := s.dav.ShareCalendarFresh(r.Context(), owner, db.UUIDString(c.ID), email, nil)
	if !writeShareResult(w, err, "task list not found") {
		return
	}
	sharerEmail := ""
	if su, e := s.q.GetUserByID(r.Context(), mustUUID(owner)); e == nil {
		sharerEmail = su.Email
	}
	if s.notify != nil && fresh { // sharing again only updates the share: no second email
		s.notify.Emit(r.Context(), db.UUIDString(share.SharedWithUser), "share.received",
			map[string]any{"NodeName": c.Name, "SharerEmail": sharerEmail, "ResourceLabel": "task list"})
	}
	writeShareOK(w)
}

// GET /me/task-lists/{id}/shares — who the list is shared with (owner only).
func (s *Server) handleListTaskListShares(w http.ResponseWriter, r *http.Request) {
	c, err := s.ownTaskList(r, r.PathValue("id"))
	if err != nil {
		writeTaskListError(w, err)
		return
	}
	infos, err := s.dav.ListCalendarShares(r.Context(), auth.UserID(r.Context()), db.UUIDString(c.ID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	type shareDTO struct {
		ID    string `json:"id"`
		Email string `json:"email"`
	}
	out := make([]shareDTO, 0, len(infos))
	for _, in := range infos {
		out = append(out, shareDTO{ID: in.ID, Email: in.Email})
	}
	writeJSON(w, http.StatusOK, out)
}

// DELETE /me/task-lists/{id}/shares/{shareId} — the owner takes access away, or the
// recipient leaves the list (their own share).
func (s *Server) handleDeleteTaskListShare(w http.ResponseWriter, r *http.Request) {
	listID := r.PathValue("id")
	lists, err := s.accessibleTaskLists(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	found := false
	for _, c := range lists {
		if db.UUIDString(c.ID) == listID {
			found = true
		}
	}
	if !found {
		writeError(w, http.StatusNotFound, "task list not found")
		return
	}
	shareID := r.PathValue("shareId")
	// the share must be one of this list's: a share id of some other collection is not found
	if sid, perr := db.ParseUUID(shareID); perr != nil {
		writeError(w, http.StatusNotFound, "share not found")
		return
	} else if sh, gerr := s.q.GetShare(r.Context(), sid); gerr != nil || db.UUIDString(sh.ResourceID) != listID {
		writeError(w, http.StatusNotFound, "share not found")
		return
	}
	switch err := s.dav.DeleteCalendarShare(r.Context(), auth.UserID(r.Context()), shareID); {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, dav.ErrNotOwner):
		writeError(w, http.StatusForbidden, "only the owner can take access away")
	case errors.Is(err, dav.ErrNotFound):
		writeError(w, http.StatusNotFound, "share not found")
	default:
		writeError(w, http.StatusInternalServerError, "failed to delete")
	}
}
