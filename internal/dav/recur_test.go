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
