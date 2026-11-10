package dav_test

import (
	"context"
	"sort"
	"strings"
	"testing"
	"time"

	"discodrive/internal/db"
)

func spanEvent(uid, props string) string {
	return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//t//EN\r\nBEGIN:VEVENT\r\nUID:" + uid +
		"\r\nDTSTAMP:20260101T000000Z\r\n" + props + "END:VEVENT\r\nEND:VCALENDAR\r\n"
}

// The month view asks for a date range. It gets the events that can fall into it — also
// the recurring ones, and those stored before spans were recorded — and not the calendar's
// whole history.
func TestCalendarObjectsInWindow(t *testing.T) {
	svc, pool, userID, _ := setupReview(t)
	ctx := context.Background()
	cal, err := svc.CreateCalendar(ctx, userID, "Личный", "")
	if err != nil {
		t.Fatal(err)
	}
	calID := db.UUIDString(cal.ID)
	put := func(uid, props string) {
		t.Helper()
		if _, err := svc.PutCalendarObject(ctx, calID, uid, spanEvent(uid, props)); err != nil {
			t.Fatalf("put %s: %v", uid, err)
		}
	}
	put("june", "DTSTART:20260612T120000Z\r\nDTEND:20260612T130000Z\r\n")
	put("last-year", "DTSTART:20250612T120000Z\r\nDTEND:20250612T130000Z\r\n")
	put("next-year", "DTSTART:20270612T120000Z\r\nDTEND:20270612T130000Z\r\n")
	put("weekly-since-2020", "DTSTART:20200106T090000Z\r\nDTEND:20200106T100000Z\r\nRRULE:FREQ=WEEKLY\r\n")
	put("weekly-from-2030", "DTSTART:20300106T090000Z\r\nDTEND:20300106T100000Z\r\nRRULE:FREQ=WEEKLY\r\n")
	put("all-day-on-the-edge", "DTSTART;VALUE=DATE:20260701\r\nDTEND;VALUE=DATE:20260702\r\n")
	put("legacy", "DTSTART:20240101T120000Z\r\nDTEND:20240101T130000Z\r\n")
	// As rows written before the migration look: no span on record.
	if _, err := pool.Exec(ctx, "UPDATE calendar_objects SET span_start = NULL, span_end = NULL, span_known = false WHERE uid = 'legacy'"); err != nil {
		t.Fatal(err)
	}

	june := func() []string {
		t.Helper()
		objs, err := svc.CalendarObjectsInWindow(ctx, calID,
			time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC))
		if err != nil {
			t.Fatal(err)
		}
		var uids []string
		for _, o := range objs {
			uids = append(uids, o.Uid)
		}
		sort.Strings(uids)
		return uids
	}
	want := "all-day-on-the-edge,june,legacy,weekly-since-2020"
	if got := strings.Join(june(), ","); got != want {
		t.Fatalf("June before the backfill: %s\nwant %s", got, want)
	}

	n, err := svc.BackfillSpans(ctx)
	if err != nil || n != 1 {
		t.Fatalf("backfill filled %d objects (err %v), want the one legacy row", n, err)
	}
	want = "all-day-on-the-edge,june,weekly-since-2020"
	if got := strings.Join(june(), ","); got != want {
		t.Fatalf("June after the backfill: %s\nwant %s", got, want)
	}
	if n, err := svc.BackfillSpans(ctx); err != nil || n != 0 {
		t.Fatalf("a second backfill filled %d objects (err %v)", n, err)
	}

	// An event moved into the window by an edit is found there.
	put("next-year", "DTSTART:20260620T120000Z\r\nDTEND:20260620T130000Z\r\n")
	want = "all-day-on-the-edge,june,next-year,weekly-since-2020"
	if got := strings.Join(june(), ","); got != want {
		t.Fatalf("June after the edit: %s\nwant %s", got, want)
	}
}
