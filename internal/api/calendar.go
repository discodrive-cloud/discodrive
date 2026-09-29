package api

import (
	"encoding/json"
	"errors"
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

// resolveCal returns the target calendar id: the requested one, when the user can access
// it, or the default VEVENT calendar when none was named. A named calendar the user
// cannot reach is a 404: falling back to the default would read, write or delete an
// event in a calendar the client did not ask for. status is 0 on success.
func (s *Server) resolveCal(r *http.Request, calID string) (string, int) {
	userID := auth.UserID(r.Context())
	if calID != "" {
		ok, err := s.dav.CanAccessCalendar(r.Context(), userID, calID)
		if err != nil {
			return "", http.StatusInternalServerError
		}
		if !ok {
			return "", http.StatusNotFound
		}
		return calID, 0
	}
	c, err := s.dav.EnsureDefaultCalendar(r.Context(), userID)
	if err != nil {
		return "", http.StatusInternalServerError
	}
	return db.UUIDString(c.ID), 0
}

// writeResolveCalError answers a resolveCal failure.
func writeResolveCalError(w http.ResponseWriter, status int) {
	if status == http.StatusNotFound {
		writeError(w, http.StatusNotFound, "calendar not found")
		return
	}
	writeError(w, http.StatusInternalServerError, "internal error")
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

// GET /me/calendar/events?start=<RFC3339>&end=<RFC3339>[&tz=<IANA zone>]
//
// tz is the browser's zone: floating times (no TZID, no Z) are read in it. Without it they
// are read in the server's zone.
func (s *Server) handleListEvents(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserID(r.Context())
	loc := time.Local
	if z, ok := loadZone(r.URL.Query().Get("tz")); ok {
		loc = z
	}
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
	if shared, serr := s.dav.SharedCalendarsForUser(r.Context(), userID); serr == nil {
		for _, sc := range shared {
			cals = append(cals, sc.Calendar)
		}
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
	writeJSON(w, http.StatusOK, out)
}

// allDaySlack widens the window for all-day events. Their dates are floating: the date is
// the same in every zone, but the window arrives as two instants of the browser's zone,
// which the server does not know. An all-day date within 14 hours (the widest UTC offset)
// of the window may be visible there; the client places it by its date.
const allDaySlack = 14 * time.Hour

// dateZone is the zone a date-time property is read in: UTC for a DATE (all-day values are
// floating dates, and reading them at UTC midnight keeps the date whatever the server's or
// the browser's zone), loc for anything else (a TZID or a Z overrides it anyway).
func dateZone(p *ical.Prop, loc *time.Location) *time.Location {
	if p != nil && (p.Params.Get(ical.ParamValue) == "DATE" || len(p.Value) == len("20060102")) {
		return time.UTC
	}
	return loc
}

// expandEvents expands the VEVENT components of a calendar into the occurrences that
// intersect [rangeStart, rangeEnd): an event that began before the window and is still
// going is included, as is every day of a multi-day event. All-day occurrences are
// reported at UTC midnight of their dates, the same form the single-event GET uses.
func expandEvents(cal *ical.Calendar, rangeStart, rangeEnd time.Time, loc *time.Location) []occurrence {
	normalizeTimes(cal)
	var masters []*ical.Component
	overrides := map[string]map[int64]*ical.Component{} // uid -> recID (unix) -> override component
	for _, comp := range cal.Children {
		if comp.Name != ical.CompEvent {
			continue
		}
		uid := propRaw(comp, ical.PropUID)
		if rid := comp.Props.Get(ical.PropRecurrenceID); rid != nil {
			t, e := rid.DateTime(dateZone(rid, loc))
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
	window := func(allDay bool) (time.Time, time.Time) {
		if allDay {
			return rangeStart.Add(-allDaySlack), rangeEnd.Add(allDaySlack)
		}
		return rangeStart, rangeEnd
	}
	overlaps := func(st time.Time, dur time.Duration, allDay bool) bool {
		ws, we := window(allDay)
		if dur <= 0 {
			return !st.Before(ws) && st.Before(we)
		}
		return st.Before(we) && st.Add(dur).After(ws)
	}
	stamp := func(t time.Time, allDay bool) string {
		if allDay {
			y, m, d := t.Date()
			return time.Date(y, m, d, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)
		}
		return t.Format(time.RFC3339)
	}
	var out []occurrence
	for _, m := range masters {
		uid := propRaw(m, ical.PropUID)
		ev := &ical.Event{Component: m}
		allDay := isAllDay(m)
		eloc := dateZone(m.Props.Get(ical.PropDateTimeStart), loc)
		dtstart, err := ev.DateTimeStart(eloc)
		if err != nil {
			continue
		}
		dtend, e2 := ev.DateTimeEnd(eloc)
		if e2 != nil || dtend.Before(dtstart) {
			dtend = dtstart
		}
		dur := dtend.Sub(dtstart)
		recurring := m.Props.Get(ical.PropRecurrenceRule) != nil
		var starts []time.Time
		if recurring {
			if set, serr := m.RecurrenceSet(eloc); serr == nil && set != nil {
				ws, we := window(allDay)
				// an occurrence that began up to one duration before the window is still on
				for _, st := range set.Between(ws.Add(-dur), we, true) {
					if overlaps(st, dur, allDay) {
						starts = append(starts, st)
					}
				}
			}
		} else if overlaps(dtstart, dur, allDay) {
			starts = []time.Time{dtstart}
		}
		for _, st := range starts {
			if ov, ok := overrides[uid][st.Unix()]; ok {
				ovEv := &ical.Event{Component: ov}
				ovAllDay := isAllDay(ov)
				ovLoc := dateZone(ov.Props.Get(ical.PropDateTimeStart), loc)
				os, oserr := ovEv.DateTimeStart(ovLoc)
				if oserr != nil {
					os = st
				}
				oe, oerr := ovEv.DateTimeEnd(ovLoc)
				if oerr != nil || oe.Before(os) {
					oe = os.Add(dur)
				}
				out = append(out, occurrence{
					UID:          uid,
					RecurrenceID: stamp(st, allDay),
					Summary:      propText(ov, ical.PropSummary),
					Location:     propText(ov, ical.PropLocation),
					Start:        stamp(os, ovAllDay),
					End:          stamp(oe, ovAllDay),
					AllDay:       ovAllDay,
					Recurring:    true,
				})
				continue
			}
			out = append(out, occurrence{
				UID:       uid,
				Summary:   propText(m, ical.PropSummary),
				Location:  propText(m, ical.PropLocation),
				Start:     stamp(st, allDay),
				End:       stamp(st.Add(dur), allDay),
				AllDay:    allDay,
				Recurring: recurring,
			})
		}
	}
	return out
}

// GET /me/calendar/events/{uid} — master form of the event
func (s *Server) handleGetEvent(w http.ResponseWriter, r *http.Request) {
	calID, status := s.resolveCal(r, r.URL.Query().Get("calendar_id"))
	if status != 0 {
		writeResolveCalError(w, status)
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
	// Freq: "" none; DAILY, WEEKLY, MONTHLY, YEARLY; "keep" — leave the RRULE as it is
	// (read back for a rule the presets cannot express: INTERVAL, COUNT, BYDAY, ...).
	Freq string `json:"freq"`
	// Until: the last day of the series as a date at UTC midnight (RFC3339), or "".
	// With "keep", a changed date rewrites only the UNTIL of the kept rule.
	Until      string `json:"until"`
	Alarm      string `json:"alarm"` // "" none; "keep" leave unchanged; "0|5|15|30|60|1440" minutes before start
	CalendarID string `json:"calendar_id"`
	// TZ: in a request, the browser's IANA zone — a recurring event is written in it (so
	// that it keeps its wall-clock time across DST changes) unless it already has a zone of
	// its own. In a response, the event's own TZID ("" for UTC or floating times).
	TZ string `json:"tz,omitempty"`
}

func eventToForm(uid string, cal *ical.Calendar) eventForm {
	normalizeTimes(cal)
	f := eventForm{UID: uid}
	for _, comp := range cal.Children {
		if comp.Name != ical.CompEvent || comp.Props.Get(ical.PropRecurrenceID) != nil {
			continue
		}
		ev := &ical.Event{Component: comp}
		f.Summary = propText(comp, ical.PropSummary)
		f.Location = propText(comp, ical.PropLocation)
		f.Description = propText(comp, ical.PropDescription)
		f.AllDay = isAllDay(comp)
		loc := dateZone(comp.Props.Get(ical.PropDateTimeStart), time.Local)
		// An all-day boundary is a bare date; pin it to midnight UTC so that reading
		// the form in another zone cannot move it to the neighbouring day.
		stamp := func(t time.Time) string {
			if f.AllDay {
				y, m, d := t.Date()
				return time.Date(y, m, d, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)
			}
			return t.Format(time.RFC3339)
		}
		startLoc := time.UTC
		if st, e := ev.DateTimeStart(loc); e == nil {
			f.Start = stamp(st)
			startLoc = st.Location()
		}
		if en, e := ev.DateTimeEnd(loc); e == nil {
			f.End = stamp(en)
		}
		if p := comp.Props.Get(ical.PropDateTimeStart); p != nil {
			f.TZ = p.Params.Get(ical.PropTimezoneID)
		}
		if rp := comp.Props.Get(ical.PropRecurrenceRule); rp != nil && rp.Value != "" {
			if simpleRule(rp.Value) {
				f.Freq = parseFreq(rp.Value)
			} else {
				f.Freq = "keep"
			}
			if y, m, d, ok := untilDate(rp.Value, startLoc); ok {
				f.Until = time.Date(y, m, d, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)
			}
		}
		f.Alarm = readAlarmPreset(comp)
		break
	}
	return f
}

// rruleParts splits an RRULE value into its NAME=VALUE parts, names upper-cased.
func rruleParts(rr string) [][2]string {
	var out [][2]string
	for _, part := range strings.Split(rr, ";") {
		k, v, _ := strings.Cut(strings.TrimSpace(part), "=")
		if k == "" {
			continue
		}
		out = append(out, [2]string{strings.ToUpper(k), v})
	}
	return out
}

func rrulePart(rr, name string) (string, bool) {
	for _, p := range rruleParts(rr) {
		if p[0] == name {
			return p[1], true
		}
	}
	return "", false
}

// simpleRule reports whether the presets of the form (FREQ plus an optional UNTIL) can
// express the rule without losing anything.
func simpleRule(rr string) bool {
	for _, p := range rruleParts(rr) {
		if p[0] != "FREQ" && p[0] != "UNTIL" {
			return false
		}
	}
	return true
}

// parseFreq extracts the FREQ value from an RRULE string.
func parseFreq(rr string) string {
	v, _ := rrulePart(rr, "FREQ")
	return strings.ToUpper(v)
}

// untilDate returns the calendar date of the rule's UNTIL: a DATE as written, a UTC
// date-time in loc (the zone of the event's start), a floating one as written.
func untilDate(rr string, loc *time.Location) (int, time.Month, int, bool) {
	v, ok := rrulePart(rr, "UNTIL")
	if !ok {
		return 0, 0, 0, false
	}
	var t time.Time
	var err error
	switch {
	case len(v) == len("20060102"):
		t, err = time.Parse("20060102", v)
	case strings.HasSuffix(v, "Z"):
		t, err = time.Parse("20060102T150405Z", v)
		t = t.In(loc)
	default:
		t, err = time.Parse("20060102T150405", v)
	}
	if err != nil {
		return 0, 0, 0, false
	}
	y, m, d := t.Date()
	return y, m, d, true
}

// formDate reads a date sent by the form: RFC3339 (the date as written, the web sends UTC
// midnight) or YYYY-MM-DD.
func formDate(s string) (int, time.Month, int, bool) {
	if s == "" {
		return 0, 0, 0, false
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		if t, err = time.Parse("2006-01-02", s); err != nil {
			return 0, 0, 0, false
		}
	}
	y, m, d := t.Date()
	return y, m, d, true
}

// formatUntil renders the last day of a series as an UNTIL value: a DATE for an all-day
// series (RFC 5545 wants the same value type as DTSTART), otherwise the end of that day in
// the event's zone, in UTC.
func formatUntil(y int, m time.Month, d int, allDay bool, loc *time.Location) string {
	if allDay {
		return time.Date(y, m, d, 0, 0, 0, 0, time.UTC).Format("20060102")
	}
	return time.Date(y, m, d, 23, 59, 59, 0, loc).UTC().Format("20060102T150405Z")
}

// setUntil replaces the UNTIL of a rule ("" removes it); COUNT goes, the two exclude each
// other. Every other part stays as it is, in its place.
func setUntil(rr, until string) string {
	var parts []string
	placed := false
	for _, p := range rruleParts(rr) {
		switch p[0] {
		case "COUNT":
			if until == "" {
				parts = append(parts, p[0]+"="+p[1])
			}
		case "UNTIL":
			if until != "" && !placed {
				parts = append(parts, "UNTIL="+until)
				placed = true
			}
		default:
			parts = append(parts, p[0]+"="+p[1])
		}
	}
	if until != "" && !placed {
		parts = append(parts, "UNTIL="+until)
	}
	return strings.Join(parts, ";")
}

// errBadEventStart is answered with 400: a missing or malformed start used to be written
// as year 1 (00010101T000000Z).
var errBadEventStart = errors.New("start must be an RFC3339 date-time")

// eventZone is the zone a recurring event is written in: the zone it already has (edits
// from a browser that reports UTC — Firefox with resistFingerprinting — must not move it),
// else the browser's, else UTC.
func eventZone(oldTZID, browserTZ string) *time.Location {
	if oldTZID != "" {
		if loc, ok := resolveTZName(oldTZID); ok {
			return loc
		}
	}
	if loc, ok := loadZone(browserTZ); ok {
		return loc
	}
	return time.UTC
}

func (s *Server) handleCreateEvent(w http.ResponseWriter, r *http.Request) {
	var form eventForm
	if err := json.NewDecoder(r.Body).Decode(&form); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	calID, status := s.resolveCal(r, form.CalendarID)
	if status != 0 {
		writeResolveCalError(w, status)
		return
	}
	uid := newContactUID()
	cal := ical.NewCalendar()
	cal.Props.SetText(ical.PropProductID, "-//discodrive//web//RU")
	cal.Props.SetText(ical.PropVersion, "2.0")
	ev := ical.NewEvent()
	ev.Props.SetText(ical.PropUID, uid)
	ev.Props.SetDateTime(ical.PropDateTimeStamp, time.Now().UTC())
	if err := applyEventForm(ev, form); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	cal.Children = append(cal.Children, ev.Component)
	if err := s.putCal(r, calID, uid, cal); err != nil {
		writePutCalError(w, err)
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
	calID, status := s.resolveCal(r, form.CalendarID)
	if status != 0 {
		writeResolveCalError(w, status)
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
			if err := applyEventForm(&ical.Event{Component: comp}, form); err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			break
		}
	}
	if err := s.putCal(r, calID, uid, cal); err != nil {
		writePutCalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"uid": uid})
}

func (s *Server) handleDeleteEvent(w http.ResponseWriter, r *http.Request) {
	calID, status := s.resolveCal(r, r.URL.Query().Get("calendar_id"))
	if status != 0 {
		writeResolveCalError(w, status)
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

// putCal stores a calendar object built or edited by the web handlers, with a VTIMEZONE
// for every zone it references.
func (s *Server) putCal(r *http.Request, calID, uid string, cal *ical.Calendar) error {
	ensureVTimezones(cal)
	var b strings.Builder
	if err := ical.NewEncoder(&b).Encode(cal); err != nil {
		return err
	}
	_, err := s.dav.PutCalendarObject(r.Context(), calID, uid, b.String())
	return err
}

func writePutCalError(w http.ResponseWriter, err error) {
	if errors.Is(err, dav.ErrUIDConflict) {
		writeError(w, http.StatusConflict, "another object in this calendar has the same UID")
		return
	}
	writeError(w, http.StatusInternalServerError, "failed to save")
}

// applyEventForm overwrites the editable VEVENT properties; VALARM/X-* and other fields are
// left intact, and so is the RRULE unless the recurrence was changed in the form.
func applyEventForm(ev *ical.Event, f eventForm) error {
	st, err := time.Parse(time.RFC3339, f.Start)
	if err != nil {
		return errBadEventStart
	}
	en, e := time.Parse(time.RFC3339, f.End)
	if e != nil || !en.After(st) {
		en = st.Add(time.Hour)
	}

	oldTZID, oldLoc := "", time.UTC
	if p := ev.Props.Get(ical.PropDateTimeStart); p != nil {
		oldTZID = p.Params.Get(ical.PropTimezoneID)
		switch {
		case oldTZID != "":
			if loc, ok := resolveTZName(oldTZID); ok {
				oldLoc = loc
			}
		case len(p.Value) == len("20060102T150405"):
			oldLoc = time.Local // floating, read in the server's zone (eventToForm)
		}
	}
	zone := eventZone(oldTZID, f.TZ)

	// Recurrence. The form carries only presets; a rule it cannot express comes back as
	// "keep", and an unchanged preset keeps the rule as it is. Rebuilding from FREQ and
	// UNTIL alone would drop INTERVAL, COUNT, BYDAY, BYMONTHDAY, WKST and a DATE UNTIL.
	existing := ""
	if p := ev.Props.Get(ical.PropRecurrenceRule); p != nil {
		existing = p.Value
	}
	rule := ""
	switch {
	case f.Freq == "":
	case f.Freq == "keep" || (existing != "" && strings.EqualFold(parseFreq(existing), f.Freq)):
		if existing != "" {
			rule = existing
			fy, fm, fd, fok := formDate(f.Until)
			ey, em, ed, eok := untilDate(existing, oldLoc)
			switch {
			case fok == eok && (!fok || (fy == ey && fm == em && fd == ed)):
				// until unchanged: the rule stays byte for byte
			case !fok:
				rule = setUntil(existing, "")
			default:
				rule = setUntil(existing, formatUntil(fy, fm, fd, f.AllDay, zone))
			}
		}
	default:
		if freq, ferr := rrule.StrToFreq(strings.ToUpper(f.Freq)); ferr == nil {
			rule = "FREQ=" + freq.String()
			if y, m, d, ok := formDate(f.Until); ok {
				rule += ";UNTIL=" + formatUntil(y, m, d, f.AllDay, zone)
			}
		}
	}

	ev.Props.SetText(ical.PropSummary, f.Summary)
	setOrDel(ev.Component, ical.PropLocation, f.Location)
	setOrDel(ev.Component, ical.PropDescription, f.Description)
	switch {
	case f.AllDay:
		// DATE values carry no zone: the calendar date of the value as written is the date.
		// DTEND is exclusive and must follow DTSTART: a one-day event ends the next day.
		if sy, sm, sd := st.Date(); !dateBefore(sy, sm, sd, en) {
			en = time.Date(sy, sm, sd+1, 0, 0, 0, 0, time.UTC)
		}
		ev.Props.SetDate(ical.PropDateTimeStart, st)
		ev.Props.SetDate(ical.PropDateTimeEnd, en)
	case rule != "":
		// A series keeps its wall-clock time across DST only in a named zone: in UTC a
		// weekly 10:00 meeting in Berlin moves to 11:00 for half of the year.
		ev.Props.SetDateTime(ical.PropDateTimeStart, st.In(zone))
		ev.Props.SetDateTime(ical.PropDateTimeEnd, en.In(zone))
	default:
		// UTC, always. go-ical writes a non-UTC time as TZID=<Location.String()>, and
		// time.Parse gives an offset-only value an unnamed zone — the result is
		// "TZID=:20260805T100000": an empty TZID and a floating time that every client,
		// this server included, then reads in its own zone. Relative VALARM triggers
		// hang off DTSTART, so the reminders drift (or never fire) along with it.
		ev.Props.SetDateTime(ical.PropDateTimeStart, st.UTC())
		ev.Props.SetDateTime(ical.PropDateTimeEnd, en.UTC())
	}
	// RRULE is a RECUR value: SetText must NOT be used — it escapes ';' as '\;' and adds
	// VALUE=TEXT, and RecurrenceSet then fails to parse the rule.
	ev.Props.Del(ical.PropRecurrenceRule)
	if rule != "" {
		ev.Props.Set(rawProp(ical.PropRecurrenceRule, rule))
	}
	applyAlarmPreset(ev.Component, f.Alarm, f.Summary)
	return nil
}

// dateBefore reports whether the date y-m-d (as written) comes before the date of t (as
// written).
func dateBefore(y int, m time.Month, d int, t time.Time) bool {
	ty, tm, td := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC).Before(time.Date(ty, tm, td, 0, 0, 0, 0, time.UTC))
}

func setOrDel(comp *ical.Component, name, val string) {
	if val == "" {
		comp.Props.Del(name)
		return
	}
	comp.Props.SetText(name, val)
}
