package dav

import (
	"strings"
	"testing"

	"github.com/emersion/go-ical"
)

func TestBoundedRecurrence(t *testing.T) {
	for rule, want := range map[string]bool{
		"":                          true,
		"FREQ=DAILY":                true,
		"FREQ=WEEKLY;BYDAY=MO,WE":   true,
		"FREQ=HOURLY":               true,
		"FREQ=HOURLY;BYMINUTE=0,30": true,
		"FREQ=HOURLY;BYMINUTE=0,1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21,22,23,24,25,26,27,28,29,30,31,32,33,34,35,36,37,38,39,40,41,42,43,44,45,46,47;BYSECOND=0,30": false,
		"FREQ=DAILY;BYHOUR=9,12,17": true,
		"FREQ=DAILY;BYHOUR=0,1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21,22,23;BYMINUTE=0,15,30,45": true,
		"FREQ=MINUTELY;COUNT=30":               true,
		"FREQ=SECONDLY":                        false,
		"FREQ=MINUTELY":                        false,
		"FREQ=MINUTELY;COUNT=1000000":          false,
		"FREQ=SECONDLY;UNTIL=20270101T000000Z": false,
		"FREQ=YEARLY;BYHOUR=0,1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21,22,23;BYMINUTE=0,1,2,3,4,5,6,7,8,9;BYSECOND=0,1": false,
	} {
		lines := "BEGIN:VEVENT\r\nUID:e\r\nDTSTAMP:20260101T000000Z\r\nDTSTART:20260101T000000Z\r\n"
		if rule != "" {
			lines += "RRULE:" + rule + "\r\n"
		}
		cal, err := ical.NewDecoder(strings.NewReader("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//t//EN\r\n" + lines + "END:VEVENT\r\nEND:VCALENDAR\r\n")).Decode()
		if err != nil {
			t.Fatalf("%s: %v", rule, err)
		}
		if got := CalendarBounded(cal); got != want {
			t.Errorf("%q: bounded = %v, want %v", rule, got, want)
		}
	}
}

// An object may hold any number of events with one UID; each recurring one costs a
// rrule-go run that cannot be cancelled, so past a handful the object is not expanded.
func TestCalendarBounded_ManyRecurringEvents(t *testing.T) {
	build := func(n int) *ical.Calendar {
		body := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//t//EN\r\n"
		for i := 0; i < n; i++ {
			body += "BEGIN:VEVENT\r\nUID:e\r\nDTSTAMP:20260101T000000Z\r\nDTSTART:20260101T000000Z\r\nRRULE:FREQ=YEARLY;BYMONTH=2;BYMONTHDAY=30\r\nEND:VEVENT\r\n"
		}
		cal, err := ical.NewDecoder(strings.NewReader(body + "END:VCALENDAR\r\n")).Decode()
		if err != nil {
			t.Fatal(err)
		}
		return cal
	}
	if !CalendarBounded(build(MaxRecurringPerObject)) {
		t.Fatalf("%d recurring events must still be bounded", MaxRecurringPerObject)
	}
	if CalendarBounded(build(MaxRecurringPerObject + 1)) {
		t.Fatalf("%d recurring events must not be expanded", MaxRecurringPerObject+1)
	}
}
