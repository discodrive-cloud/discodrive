package api

import (
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-ical"
)

func mustTime(s string) time.Time { tm, _ := time.Parse(time.RFC3339, s); return tm }

func encodeComp(t *testing.T, comp *ical.Component) string {
	t.Helper()
	cal := ical.NewCalendar()
	cal.Props.SetText(ical.PropProductID, "-//t//EN")
	cal.Props.SetText(ical.PropVersion, "2.0")
	// the event needs UID/DTSTAMP for a valid encode
	comp.Props.SetText(ical.PropUID, "e1")
	comp.Props.SetDateTime(ical.PropDateTimeStamp, mustTime("2026-06-11T00:00:00Z"))
	cal.Children = append(cal.Children, comp)
	var b strings.Builder
	if err := ical.NewEncoder(&b).Encode(cal); err != nil {
		t.Fatalf("encode: %v", err)
	}
	return b.String()
}

func TestApplyAlarmPresetAddsRelative(t *testing.T) {
	comp := ical.NewComponent(ical.CompEvent)
	applyAlarmPreset(comp, "15", "Встреча")
	out := encodeComp(t, comp)
	if !strings.Contains(out, "BEGIN:VALARM") || !strings.Contains(out, "TRIGGER:-PT15M") {
		t.Fatalf("expected VALARM with TRIGGER:-PT15M:\n%s", out)
	}
	if !strings.Contains(out, "ACTION:DISPLAY") {
		t.Fatalf("expected ACTION:DISPLAY:\n%s", out)
	}
	if strings.Contains(out, "VALUE=TEXT") {
		t.Fatalf("TRIGGER serialized as TEXT:\n%s", out)
	}
}

func TestApplyAlarmPresetZero(t *testing.T) {
	comp := ical.NewComponent(ical.CompEvent)
	applyAlarmPreset(comp, "0", "X")
	out := encodeComp(t, comp)
	if !strings.Contains(out, "TRIGGER:PT0S") {
		t.Fatalf("expected TRIGGER:PT0S:\n%s", out)
	}
}

func TestApplyAlarmPresetEmptyRemoves(t *testing.T) {
	comp := ical.NewComponent(ical.CompEvent)
	applyAlarmPreset(comp, "15", "X")
	applyAlarmPreset(comp, "", "X")
	out := encodeComp(t, comp)
	if strings.Contains(out, "VALARM") {
		t.Fatalf("VALARM must be removed:\n%s", out)
	}
}

func TestApplyAlarmPresetKeepPreserves(t *testing.T) {
	comp := ical.NewComponent(ical.CompEvent)
	// an arbitrary "device" VALARM that is not in the preset list
	va := ical.NewComponent(ical.CompAlarm)
	va.Props.SetText(ical.PropAction, "DISPLAY")
	va.Props.SetText(ical.PropDescription, "Dev")
	trig := ical.NewProp(ical.PropTrigger)
	trig.Value = "-PT2H"
	va.Props.Set(trig)
	comp.Children = append(comp.Children, va)

	applyAlarmPreset(comp, "keep", "X")
	out := encodeComp(t, comp)
	if !strings.Contains(out, "TRIGGER:-PT2H") {
		t.Fatalf("keep must preserve the original VALARM:\n%s", out)
	}
}

func TestReadAlarmPreset(t *testing.T) {
	// no VALARM → ""
	c0 := ical.NewComponent(ical.CompEvent)
	if got := readAlarmPreset(c0); got != "" {
		t.Fatalf("no VALARM → expected \"\", got %q", got)
	}
	// one -PT15M → "15"
	c1 := ical.NewComponent(ical.CompEvent)
	applyAlarmPreset(c1, "15", "X")
	if got := readAlarmPreset(c1); got != "15" {
		t.Fatalf("single -PT15M → expected \"15\", got %q", got)
	}
	// two VALARMs → "keep"
	c2 := ical.NewComponent(ical.CompEvent)
	applyAlarmPreset(c2, "15", "X")
	va := ical.NewComponent(ical.CompAlarm)
	va.Props.SetText(ical.PropAction, "DISPLAY")
	va.Props.SetText(ical.PropDescription, "X")
	tr := ical.NewProp(ical.PropTrigger)
	tr.Value = "-PT30M"
	va.Props.Set(tr)
	c2.Children = append(c2.Children, va)
	if got := readAlarmPreset(c2); got != "keep" {
		t.Fatalf("two VALARMs → expected \"keep\", got %q", got)
	}
	// absolute trigger → "keep"
	c3 := ical.NewComponent(ical.CompEvent)
	va3 := ical.NewComponent(ical.CompAlarm)
	va3.Props.SetText(ical.PropAction, "DISPLAY")
	tr3 := ical.NewProp(ical.PropTrigger)
	tr3.Value = "20260611T090000Z"
	va3.Props.Set(tr3)
	c3.Children = append(c3.Children, va3)
	if got := readAlarmPreset(c3); got != "keep" {
		t.Fatalf("absolute trigger → expected \"keep\", got %q", got)
	}
}

func TestParseTriggerMinutes(t *testing.T) {
	cases := map[string]struct {
		min int
		ok  bool
	}{
		"-PT15M":           {15, true},
		"-PT1H":            {60, true},
		"-P1D":             {1440, true},
		"-PT24H":           {1440, true},
		"PT0S":             {0, true},
		"-PT1H30M":         {90, true},
		"20260611T090000Z": {0, false},
		"nonsense":         {0, false},
	}
	for in, want := range cases {
		min, ok := parseTriggerMinutes(in)
		if ok != want.ok || (ok && min != want.min) {
			t.Fatalf("parseTriggerMinutes(%q) = (%d,%v), expected (%d,%v)", in, min, ok, want.min, want.ok)
		}
	}
}

// A reminder that fires after the start, or counts from the end of the event, is not one
// of the "N minutes before" presets: shown as one, saving the form would rewrite it into
// a reminder before the start.
func TestReadAlarmPresetLeavesOtherRemindersAlone(t *testing.T) {
	with := func(value, related string) *ical.Component {
		c := ical.NewComponent(ical.CompEvent)
		va := ical.NewComponent(ical.CompAlarm)
		va.Props.SetText(ical.PropAction, "DISPLAY")
		tr := ical.NewProp(ical.PropTrigger)
		tr.Value = value
		if related != "" {
			tr.Params.Set("RELATED", related)
		}
		va.Props.Set(tr)
		c.Children = append(c.Children, va)
		return c
	}
	for _, c := range []struct{ value, related, want string }{
		{"-PT15M", "", "15"},
		{"-PT15M", "START", "15"},
		{"PT0S", "", "0"},
		{"PT15M", "", "keep"},
		{"+PT15M", "", "keep"},
		{"-PT15M", "END", "keep"},
	} {
		if got := readAlarmPreset(with(c.value, c.related)); got != c.want {
			t.Errorf("TRIGGER%s:%s reads as %q, want %q", c.related, c.value, got, c.want)
		}
	}
}
