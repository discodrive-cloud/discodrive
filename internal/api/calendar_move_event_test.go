package api

import (
	"context"
	"net/http"
	"testing"

	"discodrive/internal/db"
)

// The form sends the calendar chosen in it. Choosing another one for an existing event
// used to look the event up in the new calendar: "event not found". Naming the calendar
// the event is in (?calendar_id=) moves it.
func TestUpdateEventMovesItToAnotherCalendar(t *testing.T) {
	h := newCalHarness(t)
	home := h.calendarOf(t, h.uidA)
	work, err := h.dav.CreateCalendar(context.Background(), h.uidA, "Work", "")
	if err != nil {
		t.Fatal(err)
	}
	workID := db.UUIDString(work.ID)

	rec, m := h.do(t, h.tokA, "POST", "/me/calendar/events", map[string]any{
		"summary": "standup", "start": "2026-08-05T10:00:00Z", "end": "2026-08-05T11:00:00Z", "calendar_id": home})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d", rec.Code)
	}
	uid := m["uid"].(string)

	rec, _ = h.do(t, h.tokA, "PUT", "/me/calendar/events/"+uid+"?calendar_id="+home, map[string]any{
		"summary": "standup", "start": "2026-08-05T10:00:00Z", "end": "2026-08-05T11:00:00Z", "alarm": "keep", "calendar_id": workID})
	if rec.Code != http.StatusOK {
		t.Fatalf("move: %d %s", rec.Code, rec.Body)
	}
	if rec, got := h.do(t, h.tokA, "GET", "/me/calendar/events/"+uid+"?calendar_id="+workID, nil); rec.Code != http.StatusOK || got["summary"] != "standup" {
		t.Fatalf("the event is not in the new calendar: %d %v", rec.Code, got)
	}
	if rec, _ := h.do(t, h.tokA, "GET", "/me/calendar/events/"+uid+"?calendar_id="+home, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("the event is still in the old calendar: %d", rec.Code)
	}
}
