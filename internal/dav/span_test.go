package dav

import (
	"testing"
	"time"
)

func ics(body string) string {
	return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//t//EN\r\n" + body + "END:VCALENDAR\r\n"
}

func vevent(props string) string {
	return "BEGIN:VEVENT\r\nUID:u\r\nDTSTAMP:20260101T000000Z\r\n" + props + "END:VEVENT\r\n"
}

func at(s string) time.Time {
	t, err := time.Parse("2006-01-02 15:04", s)
	if err != nil {
		panic(err)
	}
	return t
}

// The span is what the month view filters on in SQL: no occurrence of the object may fall
// outside it, whatever time zone the event or the viewer is in. It may be wider than the
// event (the exact check still runs on what passes), never narrower.
func TestEventSpan(t *testing.T) {
	for _, c := range []struct {
		name       string
		data       string
		start, end string // "" = open
	}{
		{"a timed event in UTC", ics(vevent("DTSTART:20260612T120000Z\r\nDTEND:20260612T130000Z\r\n")),
			"2026-06-10 12:00", "2026-06-14 13:00"},
		{"a zoned event: the wall clock, any zone is within the slack",
			ics(vevent("DTSTART;TZID=Pacific/Kiritimati:20260612T120000\r\nDTEND;TZID=Pacific/Kiritimati:20260612T130000\r\n")),
			"2026-06-10 12:00", "2026-06-14 13:00"},
		{"an all-day event", ics(vevent("DTSTART;VALUE=DATE:20260612\r\nDTEND;VALUE=DATE:20260614\r\n")),
			"2026-06-10 00:00", "2026-06-16 00:00"},
		{"an all-day event without an end lasts its day", ics(vevent("DTSTART;VALUE=DATE:20260612\r\n")),
			"2026-06-10 00:00", "2026-06-15 00:00"},
		{"no end: the start", ics(vevent("DTSTART:20260612T120000Z\r\n")),
			"2026-06-10 12:00", "2026-06-14 12:00"},
		{"a recurring event is open-ended", ics(vevent("DTSTART:20260612T120000Z\r\nDTEND:20260612T130000Z\r\nRRULE:FREQ=WEEKLY\r\n")),
			"2026-06-10 12:00", ""},
		{"extra dates make it open-ended too", ics(vevent("DTSTART:20260612T120000Z\r\nRDATE:20270101T120000Z\r\n")),
			"2026-06-10 12:00", ""},
		{"a duration instead of an end: open-ended", ics(vevent("DTSTART:20260612T120000Z\r\nDURATION:P3D\r\n")),
			"2026-06-10 12:00", ""},
		{"an override moved before the series counts",
			ics(vevent("DTSTART:20260612T120000Z\r\nDTEND:20260612T130000Z\r\nRRULE:FREQ=WEEKLY\r\n") +
				vevent("RECURRENCE-ID:20260619T120000Z\r\nDTSTART:20260601T090000Z\r\nDTEND:20260601T100000Z\r\n")),
			"2026-05-30 09:00", ""},
		{"several events: from the first start to the last end",
			ics(vevent("DTSTART:20260612T120000Z\r\nDTEND:20260612T130000Z\r\n") + vevent("DTSTART:20260701T120000Z\r\nDTEND:20260702T130000Z\r\n")),
			"2026-06-10 12:00", "2026-07-04 13:00"},
		{"an end before the start is the start", ics(vevent("DTSTART:20260612T120000Z\r\nDTEND:20260611T130000Z\r\n")),
			"2026-06-10 12:00", "2026-06-14 12:00"},
		{"a task has no span: it is never filtered out", ics("BEGIN:VTODO\r\nUID:u\r\nDTSTAMP:20260101T000000Z\r\nSUMMARY:x\r\nEND:VTODO\r\n"),
			"", ""},
		{"an unreadable start: open, never filtered out", ics(vevent("DTSTART:someday\r\n")), "", ""},
		{"an event without a start: open", ics(vevent("SUMMARY:x\r\n")), "", ""},
		{"not iCalendar: open", "garbage", "", ""},
	} {
		start, end := eventSpan(c.data)
		check := func(what string, got *time.Time, want string) {
			switch {
			case want == "" && got != nil:
				t.Errorf("%s: %s = %v, want open", c.name, what, got.UTC())
			case want != "" && got == nil:
				t.Errorf("%s: %s is open, want %s", c.name, what, want)
			case want != "" && !got.Equal(at(want)):
				t.Errorf("%s: %s = %s, want %s", c.name, what, got.UTC().Format("2006-01-02 15:04"), want)
			}
		}
		check("start", start, c.start)
		check("end", end, c.end)
	}
}
