package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-ical"

	"discodrive/internal/dav"
	"discodrive/internal/db"
)

type taskForm struct {
	UID       string `json:"uid"`
	Summary   string `json:"summary"`
	Notes     string `json:"notes,omitempty"`
	Due       string `json:"due,omitempty"` // RFC3339 or ""
	DueAllDay bool   `json:"due_all_day"`   // DUE as VALUE=DATE
	Priority  int    `json:"priority"`      // 0=none, 1..9 (iCal; 1 = highest)
	Completed bool   `json:"completed"`
	ListID    string `json:"list_id,omitempty"` // the task's list; on create/update — the target list
}

// findTask locates a task by uid: in the list given by ?list=, otherwise in any of the
// user's lists (older clients — the MCP tools — address tasks by uid alone).
func (s *Server) findTask(r *http.Request, uid string) (db.Calendar, string, error) {
	if id := r.URL.Query().Get("list"); id != "" {
		c, err := s.taskList(r, id)
		if err != nil {
			return db.Calendar{}, "", err
		}
		data, _, err := s.dav.GetCalendarObject(r.Context(), db.UUIDString(c.ID), uid)
		return c, data, err
	}
	lists, err := s.accessibleTaskLists(r)
	if err != nil {
		return db.Calendar{}, "", err
	}
	for _, c := range lists {
		data, _, err := s.dav.GetCalendarObject(r.Context(), db.UUIDString(c.ID), uid)
		if err == nil {
			return c, data, nil
		}
		if !errors.Is(err, dav.ErrNotFound) {
			return db.Calendar{}, "", err
		}
	}
	return db.Calendar{}, "", dav.ErrNotFound
}

// writeFindTaskError answers a findTask failure.
func writeFindTaskError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, dav.ErrNotFound):
		writeError(w, http.StatusNotFound, "task not found")
	case errors.Is(err, errNoTaskList):
		writeError(w, http.StatusNotFound, "task list not found")
	default:
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}

// setIntProp writes a numeric property (PRIORITY/PERCENT-COMPLETE) without VALUE=TEXT
// (SetText forces VALUE=TEXT for non-text types — see the RRULE fix in calendar.go).
func setIntProp(comp *ical.Component, name string, v int) {
	p := ical.NewProp(name)
	p.Value = strconv.Itoa(v)
	comp.Props.Set(p)
}

// setTaskCompleted is the shared completion-status logic used by PUT and /done.
func setTaskCompleted(todo *ical.Component, done bool) {
	if done {
		todo.Props.SetText(ical.PropStatus, "COMPLETED")
		setIntProp(todo, ical.PropPercentComplete, 100)
		todo.Props.SetDateTime(ical.PropCompleted, time.Now().UTC())
	} else {
		todo.Props.SetText(ical.PropStatus, "NEEDS-ACTION")
		todo.Props.Del(ical.PropCompleted)
		todo.Props.Del(ical.PropPercentComplete)
	}
}

// applyTaskForm overwrites editable VTODO properties; VALARM/X-* and others are left untouched.
func applyTaskForm(todo *ical.Component, f taskForm) {
	todo.Props.SetText(ical.PropSummary, f.Summary)
	setOrDel(todo, ical.PropDescription, f.Notes)
	if f.Due == "" {
		todo.Props.Del(ical.PropDue)
	} else if due, err := time.Parse(time.RFC3339, f.Due); err == nil {
		if f.DueAllDay {
			todo.Props.SetDate(ical.PropDue, due)
		} else {
			todo.Props.SetDateTime(ical.PropDue, due)
		}
	}
	if f.Priority <= 0 {
		todo.Props.Del(ical.PropPriority)
	} else {
		setIntProp(todo, ical.PropPriority, f.Priority)
	}
	setTaskCompleted(todo, f.Completed)
}

// taskToForm reads the first VTODO component from a calendar into a form struct.
func taskToForm(uid string, cal *ical.Calendar) taskForm {
	normalizeTimes(cal) // a DUE with a Windows/custom TZID would otherwise read as no due date
	f := taskForm{UID: uid}
	for _, comp := range cal.Children {
		if comp.Name != ical.CompToDo {
			continue
		}
		f.Summary = propText(comp, ical.PropSummary)
		f.Notes = propText(comp, ical.PropDescription)
		if due := comp.Props.Get(ical.PropDue); due != nil {
			f.DueAllDay = due.Params.Get(ical.ParamValue) == "DATE" || len(due.Value) == len("20060102")
			if t, e := due.DateTime(dateZone(due, time.Local)); e == nil {
				// an all-day due date is a date: midnight UTC, as for all-day events, so
				// that no zone moves it to the neighbouring day
				f.Due = t.Format(time.RFC3339)
			}
		}
		if p := propText(comp, ical.PropPriority); p != "" {
			if n, e := strconv.Atoi(p); e == nil {
				f.Priority = n
			}
		}
		f.Completed = strings.EqualFold(propText(comp, ical.PropStatus), "COMPLETED")
		break
	}
	return f
}

// GET /me/tasks[?list=id] — tasks of one list or of all lists (incomplete first; sorted by
// due date, then by name).
func (s *Server) handleListTasks(w http.ResponseWriter, r *http.Request) {
	lists, err := s.accessibleTaskLists(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if id := r.URL.Query().Get("list"); id != "" {
		c, err := s.taskList(r, id)
		if err != nil {
			writeFindTaskError(w, err)
			return
		}
		lists = []db.Calendar{c}
	}
	out := make([]taskForm, 0)
	for _, c := range lists {
		listID := db.UUIDString(c.ID)
		objs, err := s.dav.ListCalendarObjects(r.Context(), listID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		for _, o := range objs {
			cal, derr := ical.NewDecoder(strings.NewReader(o.Data)).Decode()
			if derr != nil {
				continue
			}
			hasTodo := false
			for _, c := range cal.Children {
				if c.Name == ical.CompToDo {
					hasTodo = true
					break
				}
			}
			if !hasTodo {
				continue
			}
			f := taskToForm(o.Uid, cal)
			f.ListID = listID
			out = append(out, f)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Completed != out[j].Completed {
			return !out[i].Completed
		}
		di, dj := out[i].Due, out[j].Due
		if (di == "") != (dj == "") {
			return di != "" // tasks with a due date rank above those without one
		}
		if di != dj {
			return di < dj
		}
		return out[i].Summary < out[j].Summary
	})
	writeJSON(w, http.StatusOK, out)
}

// GET /me/tasks/{uid}[?list=id]
func (s *Server) handleGetTask(w http.ResponseWriter, r *http.Request) {
	c, data, err := s.findTask(r, r.PathValue("uid"))
	if err != nil {
		writeFindTaskError(w, err)
		return
	}
	cal, derr := ical.NewDecoder(strings.NewReader(data)).Decode()
	if derr != nil {
		writeError(w, http.StatusInternalServerError, "failed to parse iCalendar")
		return
	}
	f := taskToForm(r.PathValue("uid"), cal)
	f.ListID = db.UUIDString(c.ID)
	writeJSON(w, http.StatusOK, f)
}

// POST /me/tasks {…, list_id?} — into the given list, or the default one.
func (s *Server) handleCreateTask(w http.ResponseWriter, r *http.Request) {
	var form taskForm
	if err := json.NewDecoder(r.Body).Decode(&form); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	list, err := s.taskList(r, form.ListID)
	if err != nil {
		writeFindTaskError(w, err)
		return
	}
	listID := db.UUIDString(list.ID)
	uid := newContactUID()
	cal := ical.NewCalendar()
	cal.Props.SetText(ical.PropProductID, "-//discodrive//web//RU")
	cal.Props.SetText(ical.PropVersion, "2.0")
	todo := ical.NewComponent(ical.CompToDo)
	todo.Props.SetText(ical.PropUID, uid)
	todo.Props.SetDateTime(ical.PropDateTimeStamp, time.Now().UTC())
	applyTaskForm(todo, form)
	cal.Children = append(cal.Children, todo)
	if err := s.putCal(r, listID, uid, cal); err != nil {
		writePutCalError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"uid": uid, "list_id": listID})
}

// PUT /me/tasks/{uid}[?list=id] — update an existing task; a list_id other than the
// task's own moves it to that list.
func (s *Server) handleUpdateTask(w http.ResponseWriter, r *http.Request) {
	uid := r.PathValue("uid")
	src, data, err := s.findTask(r, uid)
	if err != nil {
		writeFindTaskError(w, err)
		return
	}
	var form taskForm
	if err := json.NewDecoder(r.Body).Decode(&form); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	dst := src
	if form.ListID != "" && form.ListID != db.UUIDString(src.ID) {
		if dst, err = s.taskList(r, form.ListID); err != nil {
			writeFindTaskError(w, err)
			return
		}
	}
	cal, derr := ical.NewDecoder(strings.NewReader(data)).Decode()
	if derr != nil {
		writeError(w, http.StatusInternalServerError, "failed to parse iCalendar")
		return
	}
	for _, comp := range cal.Children {
		if comp.Name == ical.CompToDo {
			applyTaskForm(comp, form)
			break
		}
	}
	dstID := db.UUIDString(dst.ID)
	if err := s.putCal(r, dstID, uid, cal); err != nil {
		writePutCalError(w, err)
		return
	}
	if dst.ID != src.ID {
		// moved: the copy is in place, now drop the original (Reminders sees a delete + an add)
		if err := s.dav.DeleteCalendarObject(r.Context(), db.UUIDString(src.ID), uid); err != nil && !errors.Is(err, dav.ErrNotFound) {
			writeError(w, http.StatusInternalServerError, "failed to move")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"uid": uid, "list_id": dstID})
}

// PUT /me/tasks/{uid}/done[?list=id] — lightweight status toggle (modify-existing; everything else is preserved).
func (s *Server) handleToggleTask(w http.ResponseWriter, r *http.Request) {
	uid := r.PathValue("uid")
	list, data, err := s.findTask(r, uid)
	if err != nil {
		writeFindTaskError(w, err)
		return
	}
	var body struct {
		Completed bool `json:"completed"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	cal, derr := ical.NewDecoder(strings.NewReader(data)).Decode()
	if derr != nil {
		writeError(w, http.StatusInternalServerError, "failed to parse iCalendar")
		return
	}
	for _, comp := range cal.Children {
		if comp.Name == ical.CompToDo {
			setTaskCompleted(comp, body.Completed)
			break
		}
	}
	if err := s.putCal(r, db.UUIDString(list.ID), uid, cal); err != nil {
		writePutCalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"uid": uid})
}

// DELETE /me/tasks/{uid}[?list=id]
func (s *Server) handleDeleteTask(w http.ResponseWriter, r *http.Request) {
	uid := r.PathValue("uid")
	list, _, err := s.findTask(r, uid)
	if err != nil {
		writeFindTaskError(w, err)
		return
	}
	if err := s.dav.DeleteCalendarObject(r.Context(), db.UUIDString(list.ID), uid); err != nil {
		writeFindTaskError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
