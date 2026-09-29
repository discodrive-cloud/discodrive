package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"discodrive/internal/dav"
	"discodrive/internal/db"
)

type calHarness struct {
	mux        *http.ServeMux
	dav        *dav.Service
	tokA, tokB string
	uidA, uidB string
}

func newCalHarness(t *testing.T) *calHarness {
	t.Helper()
	ctx := context.Background()
	pool, q, svc := bootstrapPairingDB(t)
	tokA, _, err := svc.Register(ctx, "a@x.test", "password12")
	if err != nil {
		t.Fatalf("register a: %v", err)
	}
	tokB, _, err := svc.Register(ctx, "b@x.test", "password12")
	if err != nil {
		t.Fatalf("register b: %v", err)
	}
	d := dav.NewService(pool)
	s := &Server{auth: svc, q: q, dav: d, feedLimiter: newLoginLimiter()}
	mux := http.NewServeMux()
	for pattern, h := range map[string]http.HandlerFunc{
		"GET /me/calendars":                          s.handleListCalendars,
		"DELETE /me/calendars/{id}":                  s.handleDeleteCalendar,
		"POST /me/calendars/{id}/share":              s.handleShareCalendar,
		"DELETE /me/calendars/{id}/shares/{shareId}": s.handleDeleteCalendarShare,
		"POST /me/calendar/events":                   s.handleCreateEvent,
		"GET /me/calendar/events/{uid}":              s.handleGetEvent,
		"PUT /me/calendar/events/{uid}":              s.handleUpdateEvent,
		"DELETE /me/calendar/events/{uid}":           s.handleDeleteEvent,
		"GET /me/calendar/events":                    s.handleListEvents,
		"POST /me/contacts/share":                    s.handleShareContacts,
	} {
		mux.Handle(pattern, svc.Middleware(h))
	}
	return &calHarness{mux: mux, dav: d, tokA: tokA, tokB: tokB,
		uidA: mustUserID(t, svc, tokA), uidB: mustUserID(t, svc, tokB)}
}

func (h *calHarness) do(t *testing.T, tok, method, path string, body any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var req *http.Request
	if body != nil {
		b, _ := json.Marshal(body)
		req = httptest.NewRequest(method, path, bytesReader(b))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	return rec, m
}

func (h *calHarness) calendarOf(t *testing.T, userID string) string {
	t.Helper()
	c, err := h.dav.EnsureDefaultCalendar(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	return db.UUIDString(c.ID)
}

// A calendar_id the user cannot access is a 404: the event used to be created in (read
// from, deleted from) the user's default calendar instead.
func TestEventHandlersRefuseInaccessibleCalendar(t *testing.T) {
	h := newCalHarness(t)
	calB := h.calendarOf(t, h.uidB)
	calA := h.calendarOf(t, h.uidA)
	ev := map[string]any{"summary": "x", "start": "2026-08-05T10:00:00Z", "end": "2026-08-05T11:00:00Z", "calendar_id": calB}
	if rec, _ := h.do(t, h.tokA, "POST", "/me/calendar/events", ev); rec.Code != http.StatusNotFound {
		t.Fatalf("create in someone else's calendar: %d, want 404", rec.Code)
	}
	if objs, _ := h.dav.ListCalendarObjects(context.Background(), calA); len(objs) != 0 {
		t.Fatalf("the event landed in the default calendar: %d objects", len(objs))
	}
	// B's event, addressed by A through B's calendar id
	rec, m := h.do(t, h.tokB, "POST", "/me/calendar/events", ev)
	if rec.Code != http.StatusCreated {
		t.Fatalf("B creates: %d", rec.Code)
	}
	uid := m["uid"].(string)
	if rec, _ := h.do(t, h.tokA, "GET", "/me/calendar/events/"+uid+"?calendar_id="+calB, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("A reads B's event: %d", rec.Code)
	}
	if rec, _ := h.do(t, h.tokA, "DELETE", "/me/calendar/events/"+uid+"?calendar_id="+calB, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("A deletes B's event: %d", rec.Code)
	}
	// no calendar_id still means the default calendar
	ev["calendar_id"] = ""
	if rec, _ := h.do(t, h.tokA, "POST", "/me/calendar/events", ev); rec.Code != http.StatusCreated {
		t.Fatalf("create without calendar_id: %d", rec.Code)
	}
}

// An update with a missing start is refused and the stored event is left alone: it used
// to be saved with DTSTART 00010101T000000Z.
func TestUpdateEventRejectsMissingStart(t *testing.T) {
	h := newCalHarness(t)
	rec, m := h.do(t, h.tokA, "POST", "/me/calendar/events", map[string]any{"summary": "x", "start": "2026-08-05T10:00:00Z", "end": "2026-08-05T11:00:00Z"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d", rec.Code)
	}
	uid := m["uid"].(string)
	if rec, _ := h.do(t, h.tokA, "PUT", "/me/calendar/events/"+uid, map[string]any{"summary": "y", "start": "", "alarm": "keep"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("update without start: %d, want 400", rec.Code)
	}
	_, got := h.do(t, h.tokA, "GET", "/me/calendar/events/"+uid, nil)
	if got["start"] != "2026-08-05T10:00:00Z" || got["summary"] != "x" {
		t.Fatalf("stored event changed: %v", got)
	}
}
