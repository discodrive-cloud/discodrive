package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/emersion/go-ical"

	"discodrive/internal/auth"
	"discodrive/internal/dav"
)

// GET /cal/{file} — public ICS feed for a calendar identified by token ({file} = "{token}.ics").
// No authentication required. If a password is set, HTTP Basic auth is expected. Read-only snapshot (merged VEVENTs).
func (s *Server) handleCalendarFeed(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSuffix(r.PathValue("file"), ".ics")
	calID, passHash, ok := s.dav.CalendarByFeedToken(r.Context(), token)
	if !ok {
		http.Error(w, "link not found", http.StatusNotFound)
		return
	}
	if passHash != "" {
		_, pass, has := r.BasicAuth()
		good := false
		if has {
			good, _ = auth.VerifyPassword(pass, passHash)
		}
		if !good {
			// Rate-limit failed attempts per token+IP so a leaked link can't be
			// brute-forced online. Successful polls never reach this branch.
			if !s.feedLimiter.allow(token + "|" + clientIP(r)) {
				http.Error(w, "too many attempts, please try again later", http.StatusTooManyRequests)
				return
			}
			w.Header().Set("WWW-Authenticate", `Basic realm="calendar"`)
			http.Error(w, "password required", http.StatusUnauthorized)
			return
		}
	}
	objs, err := s.dav.ListCalendarObjects(r.Context(), calID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	out := ical.NewCalendar()
	out.Props.SetText(ical.PropProductID, "-//discodrive//feed//RU")
	out.Props.SetText(ical.PropVersion, "2.0")
	if c, e := s.dav.GetCalendar(r.Context(), calID); e == nil && c.Name != "" {
		p := ical.NewProp("X-WR-CALNAME")
		p.Value = c.Name
		out.Props.Set(p)
	}
	zones := map[string]bool{}
	for _, o := range objs {
		cal, derr := ical.NewDecoder(strings.NewReader(o.Data)).Decode()
		if derr != nil {
			continue
		}
		for _, comp := range cal.Children {
			switch comp.Name {
			case ical.CompEvent:
				out.Children = append(out.Children, feedEvent(comp))
			case ical.CompTimezone:
				// every object carries its own copy of the zones it uses: one per TZID
				id := propRaw(comp, ical.PropTimezoneID)
				if !zones[id] {
					zones[id] = true
					out.Children = append(out.Children, comp)
				}
			}
		}
	}
	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	// The feed may be password-protected and carries private data: no shared caches.
	w.Header().Set("Cache-Control", "private, no-store")
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	_ = ical.NewEncoder(w).Encode(out)
}

// feedBusyKeep — what is left of a PRIVATE or CONFIDENTIAL event in the public feed: when it
// is, and how it repeats. Everything else (summary, description, location, organizer, ...)
// is dropped and the event reads as "Busy".
var feedBusyKeep = map[string]bool{
	ical.PropUID: true, ical.PropDateTimeStamp: true, ical.PropDateTimeStart: true,
	ical.PropDateTimeEnd: true, ical.PropDuration: true, ical.PropRecurrenceRule: true,
	ical.PropRecurrenceDates: true, ical.PropExceptionDates: true, ical.PropRecurrenceID: true,
	ical.PropSequence: true, ical.PropTransparency: true, ical.PropStatus: true,
	ical.PropClass: true,
}

// feedEvent prepares a VEVENT for the public read-only feed: attendees (other people's
// addresses) and alarms (the owner's reminders, which subscribers' clients would fire)
// never leave; a PRIVATE or CONFIDENTIAL event is reduced to its busy time.
func feedEvent(ev *ical.Component) *ical.Component {
	class := strings.ToUpper(strings.TrimSpace(propRaw(ev, ical.PropClass)))
	private := class == "PRIVATE" || class == "CONFIDENTIAL"
	props := make(ical.Props, len(ev.Props))
	for name, ps := range ev.Props {
		if name == ical.PropAttendee || (private && !feedBusyKeep[name]) {
			continue
		}
		props[name] = ps
	}
	if private {
		props.SetText(ical.PropSummary, "Busy")
	}
	var children []*ical.Component
	for _, c := range ev.Children {
		if c.Name != ical.CompAlarm {
			children = append(children, c)
		}
	}
	return &ical.Component{Name: ev.Name, Props: props, Children: children}
}

// POST /me/calendars/{id}/feed {password?} → {token, has_password}
func (s *Server) handleCreateFeed(w http.ResponseWriter, r *http.Request) {
	owner := auth.UserID(r.Context())
	var body struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	hash := ""
	if body.Password != "" {
		h, err := auth.HashPassword(body.Password)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		hash = h
	}
	token, err := s.dav.CreateCalendarFeedLink(r.Context(), owner, r.PathValue("id"), hash)
	switch err {
	case nil:
	case dav.ErrNotOwner:
		writeError(w, http.StatusForbidden, "owner only")
		return
	case dav.ErrNotFound:
		writeError(w, http.StatusNotFound, "calendar not found")
		return
	default:
		writeError(w, http.StatusInternalServerError, "failed to create link")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"token": token, "has_password": hash != ""})
}

// GET /me/calendars/{id}/feed → [{id, token, has_password}]
func (s *Server) handleListFeeds(w http.ResponseWriter, r *http.Request) {
	links, err := s.dav.ListCalendarFeedLinks(r.Context(), auth.UserID(r.Context()), r.PathValue("id"))
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
	type dto struct {
		ID          string `json:"id"`
		Token       string `json:"token"`
		HasPassword bool   `json:"has_password"`
	}
	out := make([]dto, 0, len(links))
	for _, l := range links {
		out = append(out, dto{ID: l.ID, Token: l.Token, HasPassword: l.HasPassword})
	}
	writeJSON(w, http.StatusOK, out)
}

// DELETE /me/calendars/{id}/feed/{shareId}
func (s *Server) handleDeleteFeed(w http.ResponseWriter, r *http.Request) {
	err := s.dav.DeleteCalendarShare(r.Context(), auth.UserID(r.Context()), r.PathValue("shareId"))
	switch err {
	case nil:
		w.WriteHeader(http.StatusNoContent)
	case dav.ErrNotOwner:
		writeError(w, http.StatusForbidden, "owner only")
	case dav.ErrNotFound:
		writeError(w, http.StatusNotFound, "link not found")
	default:
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}
