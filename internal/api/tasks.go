package api

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-ical"

	"discodrive/internal/auth"
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
}

func (s *Server) taskBook(r *http.Request) (string, bool) {
	c, err := s.dav.EnsureDefaultTaskList(r.Context(), auth.UserID(r.Context()))
	if err != nil {
		return "", false
	}
	return db.UUIDString(c.ID), true
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

// GET /me/tasks — list tasks (incomplete first; sorted by due date, then by name).
func (s *Server) handleListTasks(w http.ResponseWriter, r *http.Request) {
	calID, ok := s.taskBook(r)
	if !ok {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	objs, err := s.dav.ListCalendarObjects(r.Context(), calID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := make([]taskForm, 0, len(objs))
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
		out = append(out, taskToForm(o.Uid, cal))
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

// GET /me/tasks/{uid}
func (s *Server) handleGetTask(w http.ResponseWriter, r *http.Request) {
	calID, ok := s.taskBook(r)
	if !ok {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	data, _, err := s.dav.GetCalendarObject(r.Context(), calID, r.PathValue("uid"))
	if err == dav.ErrNotFound {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	cal, derr := ical.NewDecoder(strings.NewReader(data)).Decode()
	if derr != nil {
		writeError(w, http.StatusInternalServerError, "failed to parse iCalendar")
		return
	}
	writeJSON(w, http.StatusOK, taskToForm(r.PathValue("uid"), cal))
}

// POST /me/tasks
func (s *Server) handleCreateTask(w http.ResponseWriter, r *http.Request) {
	calID, ok := s.taskBook(r)
	if !ok {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	var form taskForm
	if err := json.NewDecoder(r.Body).Decode(&form); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	uid := newContactUID()
	cal := ical.NewCalendar()
	cal.Props.SetText(ical.PropProductID, "-//discodrive//web//RU")
	cal.Props.SetText(ical.PropVersion, "2.0")
	todo := ical.NewComponent(ical.CompToDo)
	todo.Props.SetText(ical.PropUID, uid)
	todo.Props.SetDateTime(ical.PropDateTimeStamp, time.Now().UTC())
	applyTaskForm(todo, form)
	cal.Children = append(cal.Children, todo)
	if err := s.putCal(r, calID, uid, cal); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"uid": uid})
}

// PUT /me/tasks/{uid} — update an existing task.
func (s *Server) handleUpdateTask(w http.ResponseWriter, r *http.Request) {
	calID, ok := s.taskBook(r)
	if !ok {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	uid := r.PathValue("uid")
	data, _, err := s.dav.GetCalendarObject(r.Context(), calID, uid)
	if err == dav.ErrNotFound {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	var form taskForm
	if err := json.NewDecoder(r.Body).Decode(&form); err != nil {
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
			applyTaskForm(comp, form)
			break
		}
	}
	if err := s.putCal(r, calID, uid, cal); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"uid": uid})
}

// PUT /me/tasks/{uid}/done — lightweight status toggle (modify-existing; everything else is preserved).
func (s *Server) handleToggleTask(w http.ResponseWriter, r *http.Request) {
	calID, ok := s.taskBook(r)
	if !ok {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	uid := r.PathValue("uid")
	data, _, err := s.dav.GetCalendarObject(r.Context(), calID, uid)
	if err == dav.ErrNotFound {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
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
	if err := s.putCal(r, calID, uid, cal); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"uid": uid})
}

// DELETE /me/tasks/{uid}
func (s *Server) handleDeleteTask(w http.ResponseWriter, r *http.Request) {
	calID, ok := s.taskBook(r)
	if !ok {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if err := s.dav.DeleteCalendarObject(r.Context(), calID, r.PathValue("uid")); err != nil {
		if err == dav.ErrNotFound {
			writeError(w, http.StatusNotFound, "task not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
