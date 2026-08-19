package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/teambition/rrule-go"

	"discodrive/internal/auth"
	"discodrive/internal/dav"
	"discodrive/internal/db"
)

type occurrence struct {
	UID          string `json:"uid"`
	RecurrenceID string `json:"recurrence_id,omitempty"`
	Summary      string `json:"summary"`
	Location     string `json:"location,omitempty"`
	Start        string `json:"start"`
	End          string `json:"end"`
	AllDay       bool   `json:"all_day"`
	Recurring    bool   `json:"recurring"`
	CalendarID   string `json:"calendar_id"`
}

// tagOccurrences sets calendar_id on all occurrences.
func tagOccurrences(occ []occurrence, calID string) {
	for i := range occ {
		occ[i].CalendarID = calID
	}
}

// resolveCal returns the target calendar id: the requested one (if the user has access)
// or the default VEVENT calendar. Replaces calBook wherever calendar_id is provided.
func (s *Server) resolveCal(r *http.Request, calID string) (string, bool) {
	userID := auth.UserID(r.Context())
	if calID != "" {
		if ok, _ := s.dav.CanAccessCalendar(r.Context(), userID, calID); ok {
			return calID, true
		}
	}
	c, err := s.dav.EnsureDefaultCalendar(r.Context(), userID)
	if err != nil {
		return "", false
	}
	return db.UUIDString(c.ID), true
}

// propText returns a property value with the iCalendar TEXT escaping undone.
// Prop.Value is the raw line content, where a comma, a semicolon, a backslash and a
// newline are backslash-escaped: handing that out shows the backslashes to the user
// and, once the value is sent back in a form, escapes them into the file a second time
// (SUMMARY:a\\, b), which repeats with every edit. Prop.Text() is not used because it
// also splits on commas — a client that wrote an unescaped one would lose the tail —
// and because it rejects the non-TEXT properties read through here (RRULE, PRIORITY);
// those never contain a backslash, so unescapeText leaves them alone.
func propText(comp *ical.Component, name string) string {
	if p := comp.Props.Get(name); p != nil {
		return unescapeText(p.Value)
	}
	return ""
}

// propRaw returns a property value as it stands in the file, without undoing the TEXT
// escaping. For the UID: the value stored in the uid column, and looked up by every
// handler that fetches an object, is the raw one (see dav.parseICal), so a UID that
// carries an escape sequence has to be handed out and taken back in that same form.
func propRaw(comp *ical.Component, name string) string {
	if p := comp.Props.Get(name); p != nil {
		return p.Value
	}
	return ""
}

// unescapeText reverses the TEXT escaping of RFC 5545 §3.3.11. A backslash that starts
// no valid sequence is kept as it is: the value came from another client, and dropping
// it would change the text.
func unescapeText(s string) string {
	if !strings.Contains(s, "\\") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		i++
		switch s[i] {
		case 'n', 'N':
			b.WriteByte('\n')
		case '\\', ';', ',':
			b.WriteByte(s[i])
		default:
			b.WriteByte('\\')
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

func isAllDay(comp *ical.Component) bool {
	if p := comp.Props.Get(ical.PropDateTimeStart); p != nil {
		return p.Params.Get(ical.ParamValue) == "DATE"
	}
	return false
}

// GET /me/calendar/events?start=<RFC3339>&end=<RFC3339>
func (s *Server) handleListEvents(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserID(r.Context())
	loc := time.Local
	start, err1 := time.Parse(time.RFC3339, r.URL.Query().Get("start"))
	end, err2 := time.Parse(time.RFC3339, r.URL.Query().Get("end"))
	if err1 != nil || err2 != nil {
		writeError(w, http.StatusBadRequest, "start and end are required (RFC3339)")
		return
	}
	cals, err := s.dav.ListCalendars(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := make([]occurrence, 0)
	for _, cal := range cals {
		if !strings.Contains(cal.Components, "VEVENT") {
			continue
		}
		calID := db.UUIDString(cal.ID)
		objs, oerr := s.dav.ListCalendarObjects(r.Context(), calID)
		if oerr != nil {
			continue
		}
		for _, o := range objs {
			decoded, derr := ical.NewDecoder(strings.NewReader(o.Data)).Decode()
			if derr != nil {
				continue
			}
			occ := expandEvents(decoded, start, end, loc)
			tagOccurrences(occ, calID)
			out = append(out, occ...)
		}
	}
	if shared, serr := s.dav.SharedCalendarsForUser(r.Context(), userID); serr == nil {
		for _, sc := range shared {
			cal := sc.Calendar
			if !strings.Contains(cal.Components, "VEVENT") {
				continue
			}
			calID := db.UUIDString(cal.ID)
			objs, oerr := s.dav.ListCalendarObjects(r.Context(), calID)
			if oerr != nil {
				continue
			}
			for _, o := range objs {
				decoded, derr := ical.NewDecoder(strings.NewReader(o.Data)).Decode()
				if derr != nil {
					continue
				}
				occ := expandEvents(decoded, start, end, loc)
				tagOccurrences(occ, calID)
				out = append(out, occ...)
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// expandEvents expands the VEVENT components of a calendar into occurrences within [rangeStart, rangeEnd].
func expandEvents(cal *ical.Calendar, rangeStart, rangeEnd time.Time, loc *time.Location) []occurrence {
	var masters []*ical.Component
	overrides := map[string]map[int64]*ical.Component{} // uid -> recID (unix) -> override component
	for _, comp := range cal.Children {
		if comp.Name != ical.CompEvent {
			continue
		}
		uid := propRaw(comp, ical.PropUID)
		if comp.Props.Get(ical.PropRecurrenceID) != nil {
			t, e := comp.Props.DateTime(ical.PropRecurrenceID, loc)
			if e != nil {
				continue
			}
			if overrides[uid] == nil {
				overrides[uid] = map[int64]*ical.Component{}
			}
			overrides[uid][t.Unix()] = comp
		} else {
			masters = append(masters, comp)
		}
	}
	var out []occurrence
	emit := func(comp *ical.Component, uid string, st time.Time, dur time.Duration, allDay, recurring bool) {
		out = append(out, occurrence{
			UID:       uid,
			Summary:   propText(comp, ical.PropSummary),
			Location:  propText(comp, ical.PropLocation),
			Start:     st.Format(time.RFC3339),
			End:       st.Add(dur).Format(time.RFC3339),
			AllDay:    allDay,
			Recurring: recurring,
		})
	}
	for _, m := range masters {
		uid := propRaw(m, ical.PropUID)
		ev := &ical.Event{Component: m}
		dtstart, err := ev.DateTimeStart(loc)
		if err != nil {
			continue
		}
		dtend, e2 := ev.DateTimeEnd(loc)
		if e2 != nil {
			dtend = dtstart
		}
		dur := dtend.Sub(dtstart)
		allDay := isAllDay(m)
		recurring := m.Props.Get(ical.PropRecurrenceRule) != nil
		var starts []time.Time
		if recurring {
			if set, serr := m.RecurrenceSet(loc); serr == nil && set != nil {
				starts = set.Between(rangeStart, rangeEnd, true)
			}
		} else if !dtstart.Before(rangeStart) && dtstart.Before(rangeEnd) {
			starts = []time.Time{dtstart}
		}
		for _, st := range starts {
			if ov, ok := overrides[uid][st.Unix()]; ok {
				ovEv := &ical.Event{Component: ov}
				os, _ := ovEv.DateTimeStart(loc)
				oe, oerr := ovEv.DateTimeEnd(loc)
				if oerr != nil {
					oe = os.Add(dur)
				}
				out = append(out, occurrence{
					UID:          uid,
					RecurrenceID: st.Format(time.RFC3339),
					Summary:      propText(ov, ical.PropSummary),
					Location:     propText(ov, ical.PropLocation),
					Start:        os.Format(time.RFC3339),
					End:          oe.Format(time.RFC3339),
					AllDay:       isAllDay(ov),
					Recurring:    true,
				})
				continue
			}
			emit(m, uid, st, dur, allDay, recurring)
		}
	}
	return out
}

// GET /me/calendar/events/{uid} — master form of the event
func (s *Server) handleGetEvent(w http.ResponseWriter, r *http.Request) {
	calID, ok := s.resolveCal(r, r.URL.Query().Get("calendar_id"))
	if !ok {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	data, _, err := s.dav.GetCalendarObject(r.Context(), calID, r.PathValue("uid"))
	if err == dav.ErrNotFound {
		writeError(w, http.StatusNotFound, "event not found")
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
	form := eventToForm(r.PathValue("uid"), cal)
	form.CalendarID = calID
	writeJSON(w, http.StatusOK, form)
}

type eventForm struct {
	UID         string `json:"uid"`
	Summary     string `json:"summary"`
	Location    string `json:"location"`
	Description string `json:"description"`
	Start       string `json:"start"`
	End         string `json:"end"`
	AllDay      bool   `json:"all_day"`
	Freq        string `json:"freq"`  // "", DAILY, WEEKLY, MONTHLY, YEARLY
	Until       string `json:"until"` // RFC3339 or ""
	Alarm       string `json:"alarm"` // "" none; "keep" leave unchanged; "0|5|15|30|60|1440" minutes before start
	CalendarID  string `json:"calendar_id"`
}

func eventToForm(uid string, cal *ical.Calendar) eventForm {
	f := eventForm{UID: uid}
	for _, comp := range cal.Children {
		if comp.Name != ical.CompEvent || comp.Props.Get(ical.PropRecurrenceID) != nil {
			continue
		}
		loc := time.Local
		ev := &ical.Event{Component: comp}
		f.Summary = propText(comp, ical.PropSummary)
		f.Location = propText(comp, ical.PropLocation)
		f.Description = propText(comp, ical.PropDescription)
		f.AllDay = isAllDay(comp)
		// An all-day boundary is a bare date; pin it to midnight UTC so that reading
		// the form in another zone cannot move it to the neighbouring day.
		stamp := func(t time.Time) string {
			if f.AllDay {
				y, m, d := t.Date()
				return time.Date(y, m, d, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)
			}
			return t.Format(time.RFC3339)
		}
		if st, e := ev.DateTimeStart(loc); e == nil {
			f.Start = stamp(st)
		}
		if en, e := ev.DateTimeEnd(loc); e == nil {
			f.End = stamp(en)
		}
		if rr := propText(comp, ical.PropRecurrenceRule); rr != "" {
			f.Freq = parseFreq(rr)
			f.Until = parseUntil(rr)
		}
		f.Alarm = readAlarmPreset(comp)
		break
	}
	return f
}

// parseFreq extracts the FREQ value from an RRULE string.
func parseFreq(rr string) string {
	for _, part := range strings.Split(rr, ";") {
		if strings.HasPrefix(part, "FREQ=") {
			return strings.TrimPrefix(part, "FREQ=")
		}
	}
	return ""
}

// parseUntil extracts the UNTIL value from an RRULE string and returns it as RFC3339.
func parseUntil(rr string) string {
	for _, part := range strings.Split(rr, ";") {
		if strings.HasPrefix(part, "UNTIL=") {
			if t, e := time.Parse("20060102T150405Z", strings.TrimPrefix(part, "UNTIL=")); e == nil {
				return t.Format(time.RFC3339)
			}
		}
	}
	return ""
}

func (s *Server) handleCreateEvent(w http.ResponseWriter, r *http.Request) {
	var form eventForm
	if err := json.NewDecoder(r.Body).Decode(&form); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	calID, ok := s.resolveCal(r, form.CalendarID)
	if !ok {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	uid := newContactUID()
	cal := ical.NewCalendar()
	cal.Props.SetText(ical.PropProductID, "-//discodrive//web//RU")
	cal.Props.SetText(ical.PropVersion, "2.0")
	ev := ical.NewEvent()
	ev.Props.SetText(ical.PropUID, uid)
	ev.Props.SetDateTime(ical.PropDateTimeStamp, time.Now().UTC())
	applyEventForm(ev, form)
	cal.Children = append(cal.Children, ev.Component)
	if err := s.putCal(r, calID, uid, cal); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"uid": uid})
}

func (s *Server) handleUpdateEvent(w http.ResponseWriter, r *http.Request) {
	uid := r.PathValue("uid")
	var form eventForm
	if err := json.NewDecoder(r.Body).Decode(&form); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	calID, ok := s.resolveCal(r, form.CalendarID)
	if !ok {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	data, _, err := s.dav.GetCalendarObject(r.Context(), calID, uid)
	if err == dav.ErrNotFound {
		writeError(w, http.StatusNotFound, "event not found")
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
	for _, comp := range cal.Children {
		if comp.Name == ical.CompEvent && comp.Props.Get(ical.PropRecurrenceID) == nil {
			applyEventForm(&ical.Event{Component: comp}, form)
			break
		}
	}
	if err := s.putCal(r, calID, uid, cal); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"uid": uid})
}

func (s *Server) handleDeleteEvent(w http.ResponseWriter, r *http.Request) {
	calID, ok := s.resolveCal(r, r.URL.Query().Get("calendar_id"))
	if !ok {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if err := s.dav.DeleteCalendarObject(r.Context(), calID, r.PathValue("uid")); err != nil {
		if err == dav.ErrNotFound {
			writeError(w, http.StatusNotFound, "event not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) putCal(r *http.Request, calID, uid string, cal *ical.Calendar) error {
	var b strings.Builder
	if err := ical.NewEncoder(&b).Encode(cal); err != nil {
		return err
	}
	_, err := s.dav.PutCalendarObject(r.Context(), calID, uid, b.String())
	return err
}

// applyEventForm overwrites the editable VEVENT properties; VALARM/X-* and other fields are left intact.
func applyEventForm(ev *ical.Event, f eventForm) {
	ev.Props.SetText(ical.PropSummary, f.Summary)
	setOrDel(ev.Component, ical.PropLocation, f.Location)
	setOrDel(ev.Component, ical.PropDescription, f.Description)
	st, _ := time.Parse(time.RFC3339, f.Start)
	en, e := time.Parse(time.RFC3339, f.End)
	if e != nil || !en.After(st) {
		en = st.Add(time.Hour)
	}
	if f.AllDay {
		// DATE values carry no zone: the calendar date of the value as written is the date.
		ev.Props.SetDate(ical.PropDateTimeStart, st)
		ev.Props.SetDate(ical.PropDateTimeEnd, en)
	} else {
		// UTC, always. go-ical writes a non-UTC time as TZID=<Location.String()>, and
		// time.Parse gives an offset-only value an unnamed zone — the result is
		// "TZID=:20260805T100000": an empty TZID and a floating time that every client,
		// this server included, then reads in its own zone. Relative VALARM triggers
		// hang off DTSTART, so the reminders drift (or never fire) along with it.
		ev.Props.SetDateTime(ical.PropDateTimeStart, st.UTC())
		ev.Props.SetDateTime(ical.PropDateTimeEnd, en.UTC())
	}
	// RRULE from a preset — use SetRecurrenceRule (RECUR type). SetText must NOT be used:
	// it escapes ';' as '\;' and adds VALUE=TEXT, causing RecurrenceSet to fail to parse the rule.
	ev.Props.Del(ical.PropRecurrenceRule)
	if f.Freq != "" {
		if freq, ferr := rrule.StrToFreq(f.Freq); ferr == nil {
			opt := rrule.ROption{Freq: freq}
			if f.Until != "" {
				if u, e := time.Parse(time.RFC3339, f.Until); e == nil {
					opt.Until = u.UTC()
				}
			}
			ev.Props.SetRecurrenceRule(&opt)
		}
	}
	applyAlarmPreset(ev.Component, f.Alarm, f.Summary)
}

func setOrDel(comp *ical.Component, name, val string) {
	if val == "" {
		comp.Props.Del(name)
		return
	}
	comp.Props.SetText(name, val)
}
