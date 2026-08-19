package api

import (
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-ical"
)

func TestExpandDailyCount(t *testing.T) {
	// VEVENT FREQ=DAILY;COUNT=3 starting 2026-06-12T10:00Z
	raw := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//t//EN\r\nBEGIN:VEVENT\r\nUID:e1\r\nDTSTAMP:20260611T000000Z\r\nDTSTART:20260612T100000Z\r\nDTEND:20260612T110000Z\r\nSUMMARY:Daily\r\nRRULE:FREQ=DAILY;COUNT=3\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	cal, _ := ical.NewDecoder(strings.NewReader(raw)).Decode()
	start, _ := time.Parse(time.RFC3339, "2026-06-12T00:00:00Z")
	end, _ := time.Parse(time.RFC3339, "2026-06-20T00:00:00Z")
	occ := expandEvents(cal, start, end, time.UTC)
	if len(occ) != 3 {
		t.Fatalf("expected 3 occurrences, got %d", len(occ))
	}
}

func TestExpandWithExdate(t *testing.T) {
	// same series + EXDATE on the second day → 2 occurrences
	raw := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//t//EN\r\nBEGIN:VEVENT\r\nUID:e2\r\nDTSTAMP:20260611T000000Z\r\nDTSTART:20260612T100000Z\r\nDTEND:20260612T110000Z\r\nSUMMARY:Daily\r\nRRULE:FREQ=DAILY;COUNT=3\r\nEXDATE:20260613T100000Z\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	cal, _ := ical.NewDecoder(strings.NewReader(raw)).Decode()
	start, _ := time.Parse(time.RFC3339, "2026-06-12T00:00:00Z")
	end, _ := time.Parse(time.RFC3339, "2026-06-20T00:00:00Z")
	occ := expandEvents(cal, start, end, time.UTC)
	if len(occ) != 2 {
		t.Fatalf("with EXDATE expected 2, got %d", len(occ))
	}
}

func TestExpandSingle(t *testing.T) {
	raw := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//t//EN\r\nBEGIN:VEVENT\r\nUID:e3\r\nDTSTAMP:20260611T000000Z\r\nDTSTART:20260615T120000Z\r\nDTEND:20260615T130000Z\r\nSUMMARY:Once\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	cal, _ := ical.NewDecoder(strings.NewReader(raw)).Decode()
	start, _ := time.Parse(time.RFC3339, "2026-06-14T00:00:00Z")
	end, _ := time.Parse(time.RFC3339, "2026-06-16T00:00:00Z")
	if occ := expandEvents(cal, start, end, time.UTC); len(occ) != 1 {
		t.Fatalf("single: expected 1, got %d", len(occ))
	}
}

func TestEventModifyExistingPreservesUnknown(t *testing.T) {
	// VEVENT with X-APPLE-FOO and VALARM inline; applyEventForm updates the summary; X-APPLE-FOO and VALARM are preserved
	raw := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//t//EN\r\nBEGIN:VEVENT\r\nUID:m1\r\nDTSTAMP:20260611T000000Z\r\nDTSTART:20260612T100000Z\r\nDTEND:20260612T110000Z\r\nSUMMARY:Old\r\nX-APPLE-FOO:bar\r\nBEGIN:VALARM\r\nACTION:DISPLAY\r\nTRIGGER:-PT15M\r\nEND:VALARM\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	cal, _ := ical.NewDecoder(strings.NewReader(raw)).Decode()
	for _, c := range cal.Children {
		if c.Name == ical.CompEvent {
			applyEventForm(&ical.Event{Component: c}, eventForm{Summary: "New", Start: "2026-06-12T10:00:00Z", End: "2026-06-12T11:00:00Z", Alarm: "keep"})
		}
	}
	var b strings.Builder
	_ = ical.NewEncoder(&b).Encode(cal)
	out := b.String()
	if !strings.Contains(out, "X-APPLE-FOO:bar") {
		t.Fatalf("X-APPLE-FOO lost:\n%s", out)
	}
	if !strings.Contains(out, "VALARM") {
		t.Fatalf("VALARM lost:\n%s", out)
	}
	if !strings.Contains(out, "SUMMARY:New") {
		t.Fatalf("summary was not updated")
	}
}

func TestEventAlarmRoundTrip(t *testing.T) {
	// event with X-APPLE-FOO; set a 30-minute reminder — VALARM is added, X-APPLE-FOO is intact
	raw := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//t//EN\r\nBEGIN:VEVENT\r\nUID:a1\r\nDTSTAMP:20260611T000000Z\r\nDTSTART:20260612T100000Z\r\nDTEND:20260612T110000Z\r\nSUMMARY:Встреча\r\nX-APPLE-FOO:bar\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	cal, _ := ical.NewDecoder(strings.NewReader(raw)).Decode()
	for _, c := range cal.Children {
		if c.Name == ical.CompEvent {
			applyEventForm(&ical.Event{Component: c}, eventForm{Summary: "Встреча", Start: "2026-06-12T10:00:00Z", End: "2026-06-12T11:00:00Z", Alarm: "30"})
		}
	}
	var b strings.Builder
	_ = ical.NewEncoder(&b).Encode(cal)
	out := b.String()
	if !strings.Contains(out, "TRIGGER:-PT30M") {
		t.Fatalf("expected VALARM -PT30M:\n%s", out)
	}
	if !strings.Contains(out, "X-APPLE-FOO:bar") {
		t.Fatalf("X-APPLE-FOO lost:\n%s", out)
	}
	// read back
	cal2, _ := ical.NewDecoder(strings.NewReader(out)).Decode()
	f := eventToForm("a1", cal2)
	if f.Alarm != "30" {
		t.Fatalf("readAlarmPreset via eventToForm: expected \"30\", got %q", f.Alarm)
	}
	// keep preserves the alarm
	for _, c := range cal2.Children {
		if c.Name == ical.CompEvent {
			applyEventForm(&ical.Event{Component: c}, eventForm{Summary: "Встреча", Start: "2026-06-12T10:00:00Z", End: "2026-06-12T11:00:00Z", Alarm: "keep"})
		}
	}
	var b2 strings.Builder
	_ = ical.NewEncoder(&b2).Encode(cal2)
	if !strings.Contains(b2.String(), "TRIGGER:-PT30M") {
		t.Fatalf("keep must preserve VALARM:\n%s", b2.String())
	}
}

func TestTagOccurrencesSetsCalendarID(t *testing.T) {
	raw := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//t//EN\r\nBEGIN:VEVENT\r\nUID:e1\r\nDTSTAMP:20260611T000000Z\r\nDTSTART:20260615T100000Z\r\nDTEND:20260615T110000Z\r\nSUMMARY:X\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	cal, _ := ical.NewDecoder(strings.NewReader(raw)).Decode()
	start, _ := time.Parse(time.RFC3339, "2026-06-14T00:00:00Z")
	end, _ := time.Parse(time.RFC3339, "2026-06-16T00:00:00Z")
	occ := expandEvents(cal, start, end, time.UTC)
	tagOccurrences(occ, "cal-123")
	if len(occ) != 1 || occ[0].CalendarID != "cal-123" {
		t.Fatalf("expected calendar_id=cal-123: %+v", occ)
	}
}

// TestBuiltRecurringExpands catches an RRULE serialization bug: an event CREATED via
// applyEventForm (the POST path) must have a valid RRULE (RECUR type, no VALUE=TEXT,
// no escaped ';') and expand into the correct number of occurrences.
func TestBuiltRecurringExpands(t *testing.T) {
	cal := ical.NewCalendar()
	cal.Props.SetText(ical.PropProductID, "-//t//EN")
	cal.Props.SetText(ical.PropVersion, "2.0")
	ev := ical.NewEvent()
	ev.Props.SetText(ical.PropUID, "rec1")
	ev.Props.SetDateTime(ical.PropDateTimeStamp, time.Date(2026, 6, 11, 0, 0, 0, 0, time.UTC))
	applyEventForm(ev, eventForm{
		Summary: "Зарядка",
		Start:   "2026-06-15T08:00:00Z",
		End:     "2026-06-15T08:30:00Z",
		Freq:    "DAILY",
		Until:   "2026-06-17T08:00:00Z",
	})
	cal.Children = append(cal.Children, ev.Component)

	var b strings.Builder
	if err := ical.NewEncoder(&b).Encode(cal); err != nil {
		t.Fatalf("encode: %v", err)
	}
	out := b.String()
	if !strings.Contains(out, "RRULE:FREQ=DAILY") {
		t.Fatalf("expected RRULE:FREQ=DAILY, got:\n%s", out)
	}
	if strings.Contains(out, "VALUE=TEXT") || strings.Contains(out, `\;`) {
		t.Fatalf("RRULE serialized as TEXT (escaping/VALUE=TEXT):\n%s", out)
	}

	// expansion: Jun 15, 16, 17 → 3 occurrences
	decoded, err := ical.NewDecoder(strings.NewReader(out)).Decode()
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	start, _ := time.Parse(time.RFC3339, "2026-06-14T00:00:00Z")
	end, _ := time.Parse(time.RFC3339, "2026-06-21T00:00:00Z")
	if occ := expandEvents(decoded, start, end, time.UTC); len(occ) != 3 {
		t.Fatalf("expected 3 occurrences (15/16/17), got %d", len(occ))
	}
}

// A start carrying a numeric offset must be written as UTC. go-ical would otherwise
// emit "DTSTART;TZID=:20260805T100000" — an empty TZID and a floating time that every
// client reads in its own zone, taking the relative VALARM trigger with it.
func TestEventTimesAreWrittenInUTC(t *testing.T) {
	cal := ical.NewCalendar()
	cal.Props.SetText(ical.PropProductID, "-//t//EN")
	cal.Props.SetText(ical.PropVersion, "2.0")
	ev := ical.NewEvent()
	ev.Props.SetText(ical.PropUID, "u1")
	ev.Props.SetDateTime(ical.PropDateTimeStamp, time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC))
	applyEventForm(ev, eventForm{
		Summary: "Meeting", Start: "2026-08-05T10:00:00+06:00", End: "2026-08-05T11:00:00+06:00", Alarm: "15",
	})
	cal.Children = append(cal.Children, ev.Component)
	var b strings.Builder
	_ = ical.NewEncoder(&b).Encode(cal)
	out := b.String()
	if strings.Contains(out, "TZID=") {
		t.Fatalf("a timed event must not carry TZID:\n%s", out)
	}
	if !strings.Contains(out, "DTSTART:20260805T040000Z") || !strings.Contains(out, "DTEND:20260805T050000Z") {
		t.Fatalf("expected the UTC form of the times:\n%s", out)
	}
}

// An all-day event keeps its calendar date, and the form reports it as midnight UTC
// so that reading it in another zone cannot move it to the neighbouring day.
func TestAllDayKeepsItsDate(t *testing.T) {
	cal := ical.NewCalendar()
	cal.Props.SetText(ical.PropProductID, "-//t//EN")
	cal.Props.SetText(ical.PropVersion, "2.0")
	ev := ical.NewEvent()
	ev.Props.SetText(ical.PropUID, "u2")
	ev.Props.SetDateTime(ical.PropDateTimeStamp, time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC))
	applyEventForm(ev, eventForm{
		Summary: "Vacation", Start: "2026-08-10T00:00:00Z", End: "2026-08-12T00:00:00Z", AllDay: true,
	})
	cal.Children = append(cal.Children, ev.Component)
	var b strings.Builder
	_ = ical.NewEncoder(&b).Encode(cal)
	out := b.String()
	if !strings.Contains(out, "DTSTART;VALUE=DATE:20260810") {
		t.Fatalf("expected a DATE start of 20260810:\n%s", out)
	}
	cal2, _ := ical.NewDecoder(strings.NewReader(out)).Decode()
	f := eventToForm("u2", cal2)
	if !f.AllDay || f.Start != "2026-08-10T00:00:00Z" || f.End != "2026-08-12T00:00:00Z" {
		t.Fatalf("all-day form: %+v", f)
	}
}

// TEXT properties are escaped in the file (RFC 5545 §3.3.11); the API must hand out
// the plain text. Returning the raw value showed the backslashes to the client and,
// because an edit sends the form back, escaped them into the file once more each time.
func TestEventTextIsUnescapedOnRead(t *testing.T) {
	const txt = `раз, два; три\четыре`
	form := eventForm{Summary: txt, Description: txt, Location: txt,
		Start: "2026-06-12T10:00:00Z", End: "2026-06-12T11:00:00Z"}

	// write: what the create and the update handler both do
	cal := ical.NewCalendar()
	cal.Props.SetText(ical.PropProductID, "-//t//EN")
	cal.Props.SetText(ical.PropVersion, "2.0")
	ev := ical.NewEvent()
	ev.Props.SetText(ical.PropUID, "esc1")
	ev.Props.SetDateTime(ical.PropDateTimeStamp, time.Date(2026, 6, 11, 0, 0, 0, 0, time.UTC))
	applyEventForm(ev, form)
	cal.Children = append(cal.Children, ev.Component)
	var b strings.Builder
	if err := ical.NewEncoder(&b).Encode(cal); err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !strings.Contains(b.String(), `SUMMARY:раз\, два\; три\\четыре`) {
		t.Fatalf("the file must carry the escaped text once:\n%s", b.String())
	}

	// read: the form, and the occurrences of the list endpoint
	dec, err := ical.NewDecoder(strings.NewReader(b.String())).Decode()
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	got := eventToForm("esc1", dec)
	if got.Summary != txt || got.Description != txt || got.Location != txt {
		t.Fatalf("read back: summary=%q description=%q location=%q, want %q",
			got.Summary, got.Description, got.Location, txt)
	}
	start, _ := time.Parse(time.RFC3339, "2026-06-12T00:00:00Z")
	end, _ := time.Parse(time.RFC3339, "2026-06-13T00:00:00Z")
	occ := expandEvents(dec, start, end, time.UTC)
	if len(occ) != 1 || occ[0].Summary != txt || occ[0].Location != txt {
		t.Fatalf("occurrences: %+v, want the plain text %q", occ, txt)
	}

	// an edit sends the value read back: the escaping must not grow a second layer
	for _, c := range dec.Children {
		if c.Name == ical.CompEvent {
			applyEventForm(&ical.Event{Component: c}, eventForm{Summary: got.Summary,
				Description: got.Description, Location: got.Location,
				Start: form.Start, End: form.End, Alarm: "keep"})
		}
	}
	var b2 strings.Builder
	if err := ical.NewEncoder(&b2).Encode(dec); err != nil {
		t.Fatalf("re-encode: %v", err)
	}
	if b2.String() != b.String() {
		t.Fatalf("a read-modify-write changed the file:\nbefore:\n%s\nafter:\n%s", b.String(), b2.String())
	}
}

func TestUnescapeText(t *testing.T) {
	for in, want := range map[string]string{
		`plain`:        "plain",
		`a\, b`:        "a, b",
		`a\; b`:        "a; b",
		`C:\\path`:     `C:\path`,
		`line1\nline2`: "line1\nline2",
		`a, b`:         "a, b", // an unescaped comma is text, not a list separator
		`FREQ=WEEKLY`:  "FREQ=WEEKLY",
		`50% \q kept`:  `50% \q kept`, // no valid escape: left as it is
		`trailing\`:    `trailing\`,
	} {
		if got := unescapeText(in); got != want {
			t.Errorf("unescapeText(%q) = %q, want %q", in, got, want)
		}
	}
}

// The UID is the key the object is stored and looked up under, and dav.parseICal takes
// it from the file as it stands. Undoing the escaping here would hand the client a UID
// that no longer resolves — and would break the match between a series and its override.
func TestUIDKeepsItsEscaping(t *testing.T) {
	const uid = `ev\,1`
	raw := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//t//EN\r\n" +
		"BEGIN:VEVENT\r\nUID:" + uid + "\r\nDTSTAMP:20260611T000000Z\r\n" +
		"DTSTART:20260612T100000Z\r\nDTEND:20260612T110000Z\r\nSUMMARY:Series\r\n" +
		"RRULE:FREQ=DAILY;COUNT=2\r\nEND:VEVENT\r\n" +
		"BEGIN:VEVENT\r\nUID:" + uid + "\r\nDTSTAMP:20260611T000000Z\r\n" +
		"RECURRENCE-ID:20260613T100000Z\r\nDTSTART:20260613T140000Z\r\nDTEND:20260613T150000Z\r\n" +
		`SUMMARY:Moved\, later` + "\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	cal, err := ical.NewDecoder(strings.NewReader(raw)).Decode()
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	start, _ := time.Parse(time.RFC3339, "2026-06-12T00:00:00Z")
	end, _ := time.Parse(time.RFC3339, "2026-06-20T00:00:00Z")
	occ := expandEvents(cal, start, end, time.UTC)
	if len(occ) != 2 {
		t.Fatalf("expected 2 occurrences, got %d: %+v", len(occ), occ)
	}
	for _, o := range occ {
		if o.UID != uid {
			t.Errorf("uid = %q, want the raw %q", o.UID, uid)
		}
	}
	// the override was found under that same uid, and its summary is unescaped
	if occ[1].RecurrenceID == "" || occ[1].Summary != "Moved, later" {
		t.Fatalf("override not applied: %+v", occ[1])
	}
}
