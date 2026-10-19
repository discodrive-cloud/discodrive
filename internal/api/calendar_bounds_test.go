package api

import (
	"strings"
	"testing"
	"time"

	"github.com/teambition/rrule-go"
)

// A FREQ=SECONDLY event made one week of the calendar cost ~890 MiB: rrule-go's Between
// materializes every occurrence. A rule that cannot be expanded at a bounded cost shows
// at its start only; a sub-daily timeset of every second is refused the same way.
func TestExpandEvents_UnboundedRulesAreNotExpanded(t *testing.T) {
	for _, rule := range []string{
		"FREQ=SECONDLY",
		"FREQ=YEARLY;BYHOUR=0,1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21,22,23;BYMINUTE=0,1,2,3,4,5;BYSECOND=0,1,2,3,4,5",
	} {
		cal := decodeCal(t, vcal(vevent("DTSTART:20260105T000000Z", "DTEND:20260105T000001Z", "RRULE:"+rule)))
		t0 := time.Now()
		occ := expandEvents(cal, rfc(t, "2026-01-05T00:00:00Z"), rfc(t, "2027-01-05T00:00:00Z"), time.UTC)
		if d := time.Since(t0); d > time.Second {
			t.Fatalf("%s: took %v", rule, d)
		}
		if len(occ) != 1 {
			t.Fatalf("%s: %d occurrences, want the start only", rule, len(occ))
		}
	}
}

// The iteration is counted from DTSTART, so an hourly event from long ago stops after a
// bounded number of steps instead of walking to the window.
func TestExpandEvents_StepsAreBounded(t *testing.T) {
	cal := decodeCal(t, vcal(vevent("DTSTART:19700101T000000Z", "DTEND:19700101T001000Z", "RRULE:FREQ=HOURLY")))
	t0 := time.Now()
	expandEvents(cal, rfc(t, "2026-01-01T00:00:00Z"), rfc(t, "2026-02-01T00:00:00Z"), time.UTC)
	if d := time.Since(t0); d > 2*time.Second {
		t.Fatalf("took %v", d)
	}
	// an ordinary series still expands in full
	daily := decodeCal(t, vcal(vevent("DTSTART:20200101T090000Z", "DTEND:20200101T093000Z", "RRULE:FREQ=DAILY")))
	if occ := expandEvents(daily, rfc(t, "2026-01-01T00:00:00Z"), rfc(t, "2026-02-01T00:00:00Z"), time.UTC); len(occ) != 31 {
		t.Fatalf("daily series: %d occurrences, want 31", len(occ))
	}
}

func observanceTZ(rule string, rdates int) string {
	rd := ""
	if rdates > 0 {
		v := make([]string, rdates)
		for i := range v {
			v[i] = time.Date(1990, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Hour).Format("20060102T150405")
		}
		rd = "RDATE:" + strings.Join(v, ",") + "\r\n"
	}
	return "BEGIN:VTIMEZONE\r\nTZID:Custom\r\n" +
		"BEGIN:STANDARD\r\nDTSTART:20000101T000000\r\nTZOFFSETFROM:+0200\r\nTZOFFSETTO:+0100\r\nRRULE:" + rule + "\r\n" + rd + "END:STANDARD\r\n" +
		"END:VTIMEZONE\r\n"
}

// A VTIMEZONE observance rule was evaluated with rrule-go once per date value: a sub-daily
// rule, a timeset of every second of the day or a rule that never matches cost up to
// seconds per value, times ten thousand EXDATE values.
func TestNormalizeTimes_HostileVTimezoneIsCheap(t *testing.T) {
	ex := make([]string, 10000)
	for i := range ex {
		ex[i] = time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC).AddDate(0, 0, i).Format("20060102T150405")
	}
	for _, rule := range []string{
		"FREQ=SECONDLY",
		"FREQ=YEARLY;BYHOUR=0,1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21,22,23;BYMINUTE=0,1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21,22,23,24,25,26,27,28,29,30,31,32,33,34,35,36,37,38,39,40,41,42,43,44,45,46,47,48,49,50,51,52,53,54,55,56,57,58,59",
		"FREQ=YEARLY;BYMONTH=2;BYMONTHDAY=30",
	} {
		cal := decodeCal(t, vcal(observanceTZ(rule, 5000)+vevent("DTSTART;TZID=Custom:20260101T090000", "RRULE:FREQ=DAILY", "EXDATE;TZID=Custom:"+strings.Join(ex, ","))))
		t0 := time.Now()
		normalizeTimes(cal)
		if d := time.Since(t0); d > time.Second {
			t.Fatalf("%.30s: normalizeTimes took %v", rule, d)
		}
	}
}

// The rules zones actually use give the same onsets as rrule-go did.
func TestYearlyRuleMatchesRRule(t *testing.T) {
	start := time.Date(1970, 1, 1, 2, 0, 0, 0, time.UTC)
	for _, rule := range []string{
		"FREQ=YEARLY;BYMONTH=3;BYDAY=-1SU",
		"FREQ=YEARLY;BYDAY=-1SU;BYMONTH=10",
		"FREQ=YEARLY;BYMONTH=3;BYDAY=2SU",
		"FREQ=YEARLY;BYMONTH=11;BYDAY=1SU",
		"FREQ=YEARLY;INTERVAL=1;BYMONTH=4;BYDAY=1SU;UNTIL=20060402T070000Z",
		"FREQ=YEARLY;BYMONTH=10;BYMONTHDAY=8,9,10,11,12,13,14;BYDAY=SU",
		"FREQ=YEARLY;BYMONTH=9;BYMONTHDAY=22",
		"FREQ=YEARLY;BYMONTH=3;BYDAY=-1FR;COUNT=30",
		"FREQ=YEARLY",
	} {
		yr := parseYearlyRule(rule)
		if yr == nil {
			t.Fatalf("%s: not read as a yearly rule", rule)
		}
		opt, err := rrule.StrToROptionInLocation(rule, time.UTC)
		if err != nil {
			t.Fatal(err)
		}
		opt.Dtstart = start
		rr, err := rrule.NewRRule(*opt)
		if err != nil {
			t.Fatal(err)
		}
		for wall := time.Date(1999, 1, 1, 12, 0, 0, 0, time.UTC); wall.Year() < 2031; wall = wall.AddDate(0, 0, 9) {
			want := rr.Before(wall, true)
			got, ok := yr.lastOnset(start, wall)
			if ok != !want.IsZero() || ok && !got.Equal(want) {
				t.Fatalf("%s at %s: got %v (%v), want %v", rule, wall.Format("2006-01-02"), got, ok, want)
			}
		}
	}
	for _, rule := range []string{"FREQ=SECONDLY", "FREQ=YEARLY;BYHOUR=1", "FREQ=YEARLY;BYSETPOS=1", "FREQ=YEARLY;INTERVAL=2", "FREQ=YEARLY;BYMONTH=1,2"} {
		if parseYearlyRule(rule) != nil {
			t.Errorf("%s: read as a yearly rule", rule)
		}
	}
}
