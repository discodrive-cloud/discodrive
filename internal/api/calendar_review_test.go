package api

import (
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-ical"
)

func decodeCal(t *testing.T, raw string) *ical.Calendar {
	t.Helper()
	cal, err := ical.NewDecoder(strings.NewReader(raw)).Decode()
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return cal
}

func vcal(body string) string {
	return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//t//EN\r\n" + body + "END:VCALENDAR\r\n"
}

func vevent(lines ...string) string {
	return "BEGIN:VEVENT\r\nUID:ev\r\nDTSTAMP:20260601T000000Z\r\nSUMMARY:Event\r\n" + strings.Join(lines, "\r\n") + "\r\nEND:VEVENT\r\n"
}

func rfc(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func masterOf(cal *ical.Calendar) *ical.Component {
	for _, c := range cal.Children {
		if c.Name == ical.CompEvent && c.Props.Get(ical.PropRecurrenceID) == nil {
			return c
		}
	}
	return nil
}

func encode(t *testing.T, cal *ical.Calendar) string {
	t.Helper()
	var b strings.Builder
	if err := ical.NewEncoder(&b).Encode(cal); err != nil {
		t.Fatalf("encode: %v", err)
	}
	return b.String()
}

// --- M24: the events list ---

// An event that began before the window and is still going, and every day of a multi-day
// event, belong to the window: the list used to take only the events starting inside it.
func TestListIncludesEventsOverlappingTheWindow(t *testing.T) {
	multi := decodeCal(t, vcal(vevent("DTSTART:20260809T220000Z", "DTEND:20260811T100000Z")))
	if occ := expandEvents(multi, rfc(t, "2026-08-10T00:00:00Z"), rfc(t, "2026-08-11T00:00:00Z"), time.UTC); len(occ) != 1 {
		t.Fatalf("multi-day event on its middle day: %d occurrences, want 1", len(occ))
	}
	nightly := decodeCal(t, vcal(vevent("DTSTART:20260801T230000Z", "DTEND:20260802T010000Z", "RRULE:FREQ=DAILY")))
	occ := expandEvents(nightly, rfc(t, "2026-08-10T00:00:00Z"), rfc(t, "2026-08-11T00:00:00Z"), time.UTC)
	if len(occ) != 2 || occ[0].Start != "2026-08-09T23:00:00Z" {
		t.Fatalf("a nightly 23:00–01:00 series in one day: %+v, want the one from the evening before and the one of the day", occ)
	}
	// an event that ended exactly at the window's start is not in it
	ended := decodeCal(t, vcal(vevent("DTSTART:20260809T220000Z", "DTEND:20260810T000000Z")))
	if occ := expandEvents(ended, rfc(t, "2026-08-10T00:00:00Z"), rfc(t, "2026-08-11T00:00:00Z"), time.UTC); len(occ) != 0 {
		t.Fatalf("an event ending at the window start: %+v", occ)
	}
}

// All-day occurrences are dates: the list reports them at UTC midnight like the single-event
// GET, whatever zone the server reads floating times in. West of the server's zone they
// used to land on the previous day.
func TestListReportsAllDayAtUTCMidnight(t *testing.T) {
	ny, _ := time.LoadLocation("America/New_York")
	cal := decodeCal(t, vcal(vevent("DTSTART;VALUE=DATE:20260810", "DTEND;VALUE=DATE:20260812")))
	// the New York browser's view of Aug 10: [04:00Z Aug 10, 04:00Z Aug 11)
	occ := expandEvents(cal, rfc(t, "2026-08-10T04:00:00Z"), rfc(t, "2026-08-11T04:00:00Z"), ny)
	if len(occ) != 1 || occ[0].Start != "2026-08-10T00:00:00Z" || occ[0].End != "2026-08-12T00:00:00Z" || !occ[0].AllDay {
		t.Fatalf("all-day in New York: %+v", occ)
	}
	// a Bishkek browser's view of Aug 11 (the second day): [18:00Z Aug 10, 18:00Z Aug 11)
	if occ := expandEvents(decodeCal(t, vcal(vevent("DTSTART;VALUE=DATE:20260810", "DTEND;VALUE=DATE:20260812"))),
		rfc(t, "2026-08-10T18:00:00Z"), rfc(t, "2026-08-11T18:00:00Z"), time.UTC); len(occ) != 1 {
		t.Fatalf("second day of an all-day event from UTC+6: %+v", occ)
	}
	yearly := decodeCal(t, vcal(vevent("DTSTART;VALUE=DATE:20200310", "RRULE:FREQ=YEARLY")))
	occ = expandEvents(yearly, rfc(t, "2026-03-01T05:00:00Z"), rfc(t, "2026-04-01T04:00:00Z"), ny)
	if len(occ) != 1 || occ[0].Start != "2026-03-10T00:00:00Z" || occ[0].End != "2026-03-11T00:00:00Z" {
		t.Fatalf("yearly all-day: %+v", occ)
	}
}

// --- M21: RRULE survives an edit ---

func TestEditKeepsRRuleTheFormCannotExpress(t *testing.T) {
	const rule = "FREQ=WEEKLY;INTERVAL=2;BYDAY=MO,WE;WKST=MO;COUNT=10"
	cal := decodeCal(t, vcal(vevent("DTSTART:20260803T080000Z", "DTEND:20260803T090000Z", "RRULE:"+rule)))
	f := eventToForm("ev", cal)
	if f.Freq != "keep" {
		t.Fatalf("freq for a rule the presets cannot express: %q, want keep", f.Freq)
	}
	f.Summary = "Renamed"
	if err := applyEventForm(&ical.Event{Component: masterOf(cal)}, f); err != nil {
		t.Fatal(err)
	}
	if got := masterOf(cal).Props.Get(ical.PropRecurrenceRule).Value; got != rule {
		t.Fatalf("RRULE after a title change: %q, want %q", got, rule)
	}
	// a new last day rewrites only UNTIL (COUNT goes, they exclude each other)
	f.Until = "2026-10-31T00:00:00Z"
	if err := applyEventForm(&ical.Event{Component: masterOf(cal)}, f); err != nil {
		t.Fatal(err)
	}
	if got := masterOf(cal).Props.Get(ical.PropRecurrenceRule).Value; got != "FREQ=WEEKLY;INTERVAL=2;BYDAY=MO,WE;WKST=MO;UNTIL=20261031T235959Z" {
		t.Fatalf("RRULE after a new until: %q", got)
	}
	// choosing a preset replaces the rule
	f.Freq, f.Until = "DAILY", ""
	_ = applyEventForm(&ical.Event{Component: masterOf(cal)}, f)
	if got := masterOf(cal).Props.Get(ical.PropRecurrenceRule).Value; got != "FREQ=DAILY" {
		t.Fatalf("RRULE after picking DAILY: %q", got)
	}
}

// A DATE-form UNTIL (all-day series from Apple/Google) is read back as the last day, and an
// unchanged form keeps the rule byte for byte.
func TestDateUntilIsReadAndKept(t *testing.T) {
	const rule = "FREQ=DAILY;UNTIL=20260617"
	cal := decodeCal(t, vcal(vevent("DTSTART;VALUE=DATE:20260615", "DTEND;VALUE=DATE:20260616", "RRULE:"+rule)))
	f := eventToForm("ev", cal)
	if f.Freq != "DAILY" || f.Until != "2026-06-17T00:00:00Z" {
		t.Fatalf("form: freq %q until %q", f.Freq, f.Until)
	}
	f.Location = "Home"
	_ = applyEventForm(&ical.Event{Component: masterOf(cal)}, f)
	if got := masterOf(cal).Props.Get(ical.PropRecurrenceRule).Value; got != rule {
		t.Fatalf("RRULE: %q, want %q", got, rule)
	}
	f.Until = "2026-06-20T00:00:00Z"
	_ = applyEventForm(&ical.Event{Component: masterOf(cal)}, f)
	if got := masterOf(cal).Props.Get(ical.PropRecurrenceRule).Value; got != "FREQ=DAILY;UNTIL=20260620" {
		t.Fatalf("all-day series with a new until: %q (UNTIL must stay a DATE)", got)
	}
	occ := expandEvents(cal, rfc(t, "2026-06-01T00:00:00Z"), rfc(t, "2026-07-01T00:00:00Z"), time.UTC)
	if len(occ) != 6 {
		t.Fatalf("15..20 June: %d occurrences", len(occ))
	}
}

// --- M22: TZIDs that are not IANA names ---

func TestWindowsTZIDIsReadNotDropped(t *testing.T) {
	cal := decodeCal(t, vcal(vevent("DTSTART;TZID=W. Europe Standard Time:20260715T100000",
		"DTEND;TZID=W. Europe Standard Time:20260715T110000")))
	occ := expandEvents(cal, rfc(t, "2026-07-15T00:00:00Z"), rfc(t, "2026-07-16T00:00:00Z"), time.UTC)
	if len(occ) != 1 || !rfc(t, occ[0].Start).Equal(rfc(t, "2026-07-15T08:00:00Z")) {
		t.Fatalf("Outlook event in Berlin time: %+v, want 08:00Z", occ)
	}
	f := eventToForm("ev", decodeCal(t, vcal(vevent("DTSTART;TZID=W. Europe Standard Time:20260715T100000"))))
	if f.Start == "" || !rfc(t, f.Start).Equal(rfc(t, "2026-07-15T08:00:00Z")) {
		t.Fatalf("form start %q", f.Start)
	}
}

// Outlook's "Customized Time Zone" has no name to map: the object's own VTIMEZONE says what
// the offsets are.
func TestCustomTZIDUsesTheObjectsVTimezone(t *testing.T) {
	const vtz = "BEGIN:VTIMEZONE\r\nTZID:Customized Time Zone\r\n" +
		"BEGIN:STANDARD\r\nDTSTART:16010101T030000\r\nTZOFFSETFROM:+0200\r\nTZOFFSETTO:+0100\r\nRRULE:FREQ=YEARLY;BYDAY=-1SU;BYMONTH=10\r\nEND:STANDARD\r\n" +
		"BEGIN:DAYLIGHT\r\nDTSTART:16010101T020000\r\nTZOFFSETFROM:+0100\r\nTZOFFSETTO:+0200\r\nRRULE:FREQ=YEARLY;BYDAY=-1SU;BYMONTH=3\r\nEND:DAYLIGHT\r\n" +
		"END:VTIMEZONE\r\n"
	for _, tc := range []struct{ local, want string }{
		{"20260715T100000", "2026-07-15T08:00:00Z"}, // summer: +02
		{"20260115T100000", "2026-01-15T09:00:00Z"}, // winter: +01
	} {
		cal := decodeCal(t, vcal(vtz+vevent("DTSTART;TZID=Customized Time Zone:"+tc.local, "DTEND;TZID=Customized Time Zone:"+tc.local)))
		occ := expandEvents(cal, rfc(t, "2026-01-01T00:00:00Z"), rfc(t, "2027-01-01T00:00:00Z"), time.UTC)
		if len(occ) != 1 || !rfc(t, occ[0].Start).Equal(rfc(t, tc.want)) {
			t.Fatalf("%s: %+v, want %s", tc.local, occ, tc.want)
		}
	}
}

// --- M23: recurring events keep their wall-clock time across DST ---

func TestRecurringEventIsWrittenInTheBrowserZone(t *testing.T) {
	cal := ical.NewCalendar()
	cal.Props.SetText(ical.PropProductID, "-//t//EN")
	cal.Props.SetText(ical.PropVersion, "2.0")
	ev := ical.NewEvent()
	ev.Props.SetText(ical.PropUID, "ev")
	ev.Props.SetDateTime(ical.PropDateTimeStamp, time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))
	// 10:00 in Berlin (CET) on a Monday, weekly
	if err := applyEventForm(ev, eventForm{Summary: "Standup", Start: "2026-03-02T09:00:00Z", End: "2026-03-02T09:30:00Z",
		Freq: "WEEKLY", TZ: "Europe/Berlin"}); err != nil {
		t.Fatal(err)
	}
	cal.Children = append(cal.Children, ev.Component)
	ensureVTimezones(cal)
	out := encode(t, cal)
	if !strings.Contains(out, "DTSTART;TZID=Europe/Berlin:20260302T100000") {
		t.Fatalf("DTSTART not in the browser's zone:\n%s", out)
	}
	if !strings.Contains(out, "BEGIN:VTIMEZONE") || !strings.Contains(out, "TZID:Europe/Berlin") {
		t.Fatalf("no VTIMEZONE for the TZID:\n%s", out)
	}
	occ := expandEvents(decodeCal(t, out), rfc(t, "2026-04-06T00:00:00Z"), rfc(t, "2026-04-07T00:00:00Z"), time.UTC)
	if len(occ) != 1 || !rfc(t, occ[0].Start).Equal(rfc(t, "2026-04-06T08:00:00Z")) {
		t.Fatalf("after the switch to CEST the standup must stay at 10:00 local (08:00Z): %+v", occ)
	}
	// a single event stays in UTC
	single := ical.NewEvent()
	_ = applyEventForm(single, eventForm{Summary: "Once", Start: "2026-03-02T09:00:00Z", End: "2026-03-02T09:30:00Z", TZ: "Europe/Berlin"})
	if p := single.Props.Get(ical.PropDateTimeStart); p.Params.Get(ical.PropTimezoneID) != "" || p.Value != "20260302T090000Z" {
		t.Fatalf("single event: %+v", p)
	}
}

// Firefox with resistFingerprinting reports UTC: editing a series that has a zone must keep it.
func TestEditFromUTCBrowserKeepsTheEventsZone(t *testing.T) {
	cal := decodeCal(t, vcal(vevent("DTSTART;TZID=Europe/Berlin:20260302T100000", "DTEND;TZID=Europe/Berlin:20260302T103000", "RRULE:FREQ=WEEKLY")))
	f := eventToForm("ev", cal)
	if f.TZ != "Europe/Berlin" || f.Freq != "WEEKLY" {
		t.Fatalf("form: %+v", f)
	}
	f.TZ = "UTC"
	f.Summary = "Renamed"
	_ = applyEventForm(&ical.Event{Component: masterOf(cal)}, f)
	if p := masterOf(cal).Props.Get(ical.PropDateTimeStart); p.Params.Get(ical.PropTimezoneID) != "Europe/Berlin" || p.Value != "20260302T100000" {
		t.Fatalf("DTSTART after an edit from a UTC browser: %s %+v", p.Value, p.Params)
	}
}

// A missing or malformed start is refused: it used to be written as 00010101T000000Z.
func TestApplyEventFormRejectsBadStart(t *testing.T) {
	ev := ical.NewEvent()
	for _, s := range []string{"", "tomorrow", "2026-13-01T00:00:00Z"} {
		if err := applyEventForm(ev, eventForm{Summary: "x", Start: s}); err == nil {
			t.Fatalf("start %q accepted", s)
		}
	}
	if p := ev.Props.Get(ical.PropDateTimeStart); p != nil {
		t.Fatalf("DTSTART written for a bad start: %s", p.Value)
	}
}

// The generated VTIMEZONE must describe the zone: its offsets, read back through the same
// code that reads foreign VTIMEZONEs, match Go's tz database around the year.
func TestBuiltVTimezoneMatchesTheZone(t *testing.T) {
	for _, name := range []string{"Europe/Berlin", "America/New_York", "Australia/Sydney", "Asia/Bishkek"} {
		loc, _ := time.LoadLocation(name)
		tz := buildVTimezone(name, loc, 2026)
		for m := time.January; m <= time.December; m++ {
			for _, d := range []int{1, 15, 28} {
				utc := time.Date(2026, m, d, 12, 0, 0, 0, time.UTC)
				_, want := utc.In(loc).Zone()
				l := utc.In(loc)
				wall := time.Date(l.Year(), l.Month(), l.Day(), l.Hour(), l.Minute(), 0, 0, time.UTC)
				got, ok := vtimezoneOffset(tz, wall)
				if !ok || got != want {
					t.Fatalf("%s %s: offset %d (ok=%v), want %d", name, wall.Format("2006-01-02"), got, ok, want)
				}
			}
		}
	}
}

// --- M25 (server side): an all-day due date is a date ---

func TestTaskAllDayDueIsReportedAtUTCMidnight(t *testing.T) {
	old := time.Local
	time.Local, _ = time.LoadLocation("Asia/Bishkek")
	defer func() { time.Local = old }()
	cal := decodeCal(t, vcal("BEGIN:VTODO\r\nUID:t\r\nDTSTAMP:20260601T000000Z\r\nSUMMARY:Pay\r\nDUE;VALUE=DATE:20260928\r\nEND:VTODO\r\n"))
	if f := taskToForm("t", cal); !f.DueAllDay || f.Due != "2026-09-28T00:00:00Z" {
		t.Fatalf("all-day due: %+v, want 2026-09-28T00:00:00Z", f)
	}
}
