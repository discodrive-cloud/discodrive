package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"discodrive/internal/db"
)

// DELETE /me/calendars/{id} answered 204 for a foreign or unknown id and 500 for a
// malformed one.
func TestDeleteCalendarStatusCodes(t *testing.T) {
	h := newCalHarness(t)
	calB := h.calendarOf(t, h.uidB)
	for _, tc := range []struct {
		id   string
		want int
	}{
		{"not-a-uuid", http.StatusBadRequest},
		{calB, http.StatusNotFound},
		{uuid.NewString(), http.StatusNotFound},
		{h.calendarOf(t, h.uidA), http.StatusNoContent},
	} {
		if rec, _ := h.do(t, h.tokA, "DELETE", "/me/calendars/"+tc.id, nil); rec.Code != tc.want {
			t.Fatalf("DELETE %s: %d, want %d", tc.id, rec.Code, tc.want)
		}
	}
	if _, err := h.dav.GetCalendar(context.Background(), calB); err != nil {
		t.Fatalf("B's calendar is gone: %v", err)
	}
}

// Sharing answers the same whether or not the email has an account; sharing with yourself
// is refused; the recipient can leave the share.
func TestShareCalendarResponses(t *testing.T) {
	h := newCalHarness(t)
	calA := h.calendarOf(t, h.uidA)
	known, mk := h.do(t, h.tokA, "POST", "/me/calendars/"+calA+"/share", map[string]any{"email": "b@x.test"})
	unknown, mu := h.do(t, h.tokA, "POST", "/me/calendars/"+calA+"/share", map[string]any{"email": "nobody@x.test"})
	if known.Code != unknown.Code || known.Body.String() != unknown.Body.String() || known.Code >= 300 {
		t.Fatalf("known email: %d %v; unknown email: %d %v — must be the same success", known.Code, mk, unknown.Code, mu)
	}
	if rec, _ := h.do(t, h.tokA, "POST", "/me/calendars/"+calA+"/share", map[string]any{"email": "a@x.test"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("self-share: %d, want 400", rec.Code)
	}
	if rec, _ := h.do(t, h.tokA, "POST", "/me/contacts/share", map[string]any{"email": "a@x.test"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("self-share of contacts: %d, want 400", rec.Code)
	}

	// B leaves
	req := httptest.NewRequest("GET", "/me/calendars", nil)
	req.Header.Set("Authorization", "Bearer "+h.tokB)
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	var cals []calendarDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &cals)
	var shared *calendarDTO
	for i := range cals {
		if cals[i].ID == calA {
			shared = &cals[i]
		}
	}
	if shared == nil || shared.ShareID == "" {
		t.Fatalf("B's calendar list: %s", rec.Body.String())
	}
	if rec, _ := h.do(t, h.tokB, "DELETE", "/me/calendars/"+calA+"/shares/"+shared.ShareID, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("recipient leaves: %d, want 204", rec.Code)
	}
	if ok, _ := h.dav.CanAccessCalendar(context.Background(), h.uidB, calA); ok {
		t.Fatal("B still has access")
	}
}

// The public feed must not publish what the owner marked private, other people's addresses
// or the owner's reminders, and must not be cached by shared caches.
func TestCalendarFeedRedactsPrivateData(t *testing.T) {
	svc, ownerID, ctx := setupCalendars(t)
	cal, _ := svc.CreateCalendar(ctx, ownerID, "Фид", "")
	calID := db.UUIDString(cal.ID)
	const vtz = "BEGIN:VTIMEZONE\r\nTZID:Europe/Berlin\r\nBEGIN:STANDARD\r\nDTSTART:19701025T030000\r\nTZOFFSETFROM:+0200\r\nTZOFFSETTO:+0100\r\nEND:STANDARD\r\nEND:VTIMEZONE\r\n"
	obj := func(uid, extra string) string {
		return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//t//EN\r\n" + vtz + "BEGIN:VEVENT\r\nUID:" + uid +
			"\r\nDTSTAMP:20260611T000000Z\r\nDTSTART;TZID=Europe/Berlin:20260620T100000\r\nDTEND;TZID=Europe/Berlin:20260620T110000\r\n" +
			extra + "BEGIN:VALARM\r\nACTION:DISPLAY\r\nDESCRIPTION:remind\r\nTRIGGER:-PT15M\r\nEND:VALARM\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	}
	if _, err := svc.PutCalendarObject(ctx, calID, "pub", obj("pub", "SUMMARY:Open meeting\r\nATTENDEE;CN=Ann:mailto:ann@example.com\r\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PutCalendarObject(ctx, calID, "priv", obj("priv", "SUMMARY:Doctor\r\nDESCRIPTION:diagnosis\r\nLOCATION:Clinic\r\nCLASS:PRIVATE\r\n")); err != nil {
		t.Fatal(err)
	}
	tok, _ := svc.CreateCalendarFeedLink(ctx, ownerID, calID, "")
	s := &Server{dav: svc, feedLimiter: newLoginLimiter()}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/cal/"+tok+".ics", nil)
	req.SetPathValue("file", tok+".ics")
	s.handleCalendarFeed(rec, req)
	out := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("feed: %d", rec.Code)
	}
	for _, leak := range []string{"Doctor", "diagnosis", "Clinic", "ann@example.com", "VALARM"} {
		if strings.Contains(out, leak) {
			t.Fatalf("feed leaks %q:\n%s", leak, out)
		}
	}
	if !strings.Contains(out, "Open meeting") || !strings.Contains(out, "SUMMARY:Busy") {
		t.Fatalf("feed lost the public event or the busy block:\n%s", out)
	}
	if n := strings.Count(out, "BEGIN:VTIMEZONE"); n != 1 {
		t.Fatalf("VTIMEZONE %d times, want once:\n%s", n, out)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "private, no-store" {
		t.Fatalf("Cache-Control %q", cc)
	}
}
