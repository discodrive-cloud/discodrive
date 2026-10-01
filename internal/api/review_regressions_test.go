package api

import (
	"bytes"
	"discodrive/internal/auth"
	"encoding/json"
	"fmt"
	"github.com/emersion/go-ical"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRegressionMovedRecurrenceIntoWindow(t *testing.T) {
	raw := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:series\r\nDTSTART:20261001T100000Z\r\nDTEND:20261001T110000Z\r\nRRULE:FREQ=DAILY;COUNT=2\r\nSUMMARY:original\r\nEND:VEVENT\r\nBEGIN:VEVENT\r\nUID:series\r\nRECURRENCE-ID:20261002T100000Z\r\nDTSTART:20261110T100000Z\r\nDTEND:20261110T110000Z\r\nSUMMARY:moved\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	cal, err := ical.NewDecoder(strings.NewReader(raw)).Decode()
	if err != nil {
		t.Fatal(err)
	}
	oct := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	if got := expandEvents(cal, oct, oct.AddDate(0, 1, 0), time.UTC); len(got) != 1 || got[0].Summary != "original" {
		t.Fatalf("moved-out event remains: %+v", got)
	}
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)
	got := expandEvents(cal, start, end, time.UTC)
	if len(got) != 1 || got[0].Summary != "moved" {
		t.Fatalf("moved event disappears from November: %+v", got)
	}
}

func TestRegressionFeedLimiterBlocksGuesses(t *testing.T) {
	svc, uid, ctx := setupCalendars(t)
	cal, err := svc.CreateCalendar(ctx, uid, "feed", "")
	if err != nil {
		t.Fatal(err)
	}
	hash, err := auth.HashPassword("correct")
	if err != nil {
		t.Fatal(err)
	}
	tok, err := svc.CreateCalendarFeedLink(ctx, uid, dbUUID(t, cal), hash)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{dav: svc, feedLimiter: newLoginLimiter()}
	attempt := func(password string) int {
		r := httptest.NewRequest("GET", "/cal/"+tok+".ics", nil)
		r.SetPathValue("file", tok+".ics")
		r.SetBasicAuth("x", password)
		w := httptest.NewRecorder()
		s.handleCalendarFeed(w, r)
		return w.Code
	}
	for i := 0; i < 15; i++ {
		if got := attempt("correct"); got != 200 {
			t.Fatalf("valid polls consumed budget: %d", got)
		}
	}
	for i := 0; i < 10; i++ {
		if got := attempt("wrong"); got != 401 {
			t.Fatalf("initial attempt: %d", got)
		}
	}
	if got := attempt("wrong"); got != 429 {
		t.Fatalf("limit not hit: %d", got)
	}
	if got := attempt("correct"); got != 429 {
		t.Fatalf("correct guess still tested after limit: HTTP %d", got)
	}
}

func TestRegressionBookmarkBulkBodyLimit(t *testing.T) {
	items := make([]map[string]any, 5000)
	for i := range items {
		items[i] = map[string]any{"id": fmt.Sprintf("00000000-0000-0000-0000-%012d", i+1), "parent_id": nil, "is_folder": false, "title": strings.Repeat("t", 60), "url": "https://example.org/" + strings.Repeat("u", 70), "position": i}
	}
	raw, err := json.Marshal(map[string]any{"items": items})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /me/bookmarks/bulk", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/me/bookmarks/bulk", bytes.NewReader(raw))
	limitAPIBodies(mux).ServeHTTP(w, r)
	if w.Code != 204 {
		t.Fatalf("5000 valid bookmarks (%d bytes) rejected before handler: HTTP%d", len(raw), w.Code)
	}
}

func TestSavedHTMLBodyAllowsJSONEscaping(t *testing.T) {
	// HTML is bounded after decoding, so escaped characters must not count six times.
	raw, err := json.Marshal(map[string]string{"content_html": strings.Repeat("<", 2<<20), "url": "https://example.org"})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /me/saved", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	w := httptest.NewRecorder()
	limitAPIBodies(mux).ServeHTTP(w, httptest.NewRequest("POST", "/me/saved", bytes.NewReader(raw)))
	if w.Code != 204 {
		t.Fatalf("escaped valid HTML refused: %d", w.Code)
	}
}
