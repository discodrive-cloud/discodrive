package api

import (
	"strconv"
	"strings"

	"github.com/emersion/go-ical"
)

// presets for "N minutes before start" (0 = at the time of the event).
var alarmPresetMinutes = map[int]bool{0: true, 5: true, 15: true, 30: true, 60: true, 1440: true}

// applyAlarmPreset sets a single relative reminder (VALARM) on the component.
//
//	alarm == "keep"      — leave existing VALARMs untouched;
//	alarm == ""          — remove all VALARMs;
//	alarm == "<minutes>" — replace all VALARMs with one (N minutes before start).
func applyAlarmPreset(comp *ical.Component, alarm, description string) {
	if alarm == "keep" {
		return
	}
	kept := make([]*ical.Component, 0, len(comp.Children))
	for _, c := range comp.Children {
		if c.Name != ical.CompAlarm {
			kept = append(kept, c)
		}
	}
	comp.Children = kept
	if alarm == "" {
		return
	}
	n, err := strconv.Atoi(alarm)
	if err != nil || n < 0 {
		return
	}
	va := ical.NewComponent(ical.CompAlarm)
	va.Props.SetText(ical.PropAction, "DISPLAY")
	if description == "" {
		description = "Reminder"
	}
	va.Props.SetText(ical.PropDescription, description)
	// TRIGGER — DURATION type; set via NewProp+Value, NOT SetText (which would produce VALUE=TEXT).
	trig := ical.NewProp(ical.PropTrigger)
	if n == 0 {
		trig.Value = "PT0S"
	} else {
		trig.Value = "-PT" + strconv.Itoa(n) + "M"
	}
	va.Props.Set(trig)
	comp.Children = append(comp.Children, va)
}

// readAlarmPreset returns the reminder preset for a component: "" (none), "<minutes>"
// (exactly one relative VALARM matching a known preset), or "keep" (anything else —
// multiple alarms, absolute time, or an unparseable trigger).
func readAlarmPreset(comp *ical.Component) string {
	var alarms []*ical.Component
	for _, c := range comp.Children {
		if c.Name == ical.CompAlarm {
			alarms = append(alarms, c)
		}
	}
	if len(alarms) == 0 {
		return ""
	}
	if len(alarms) > 1 {
		return "keep"
	}
	trig := alarms[0].Props.Get(ical.PropTrigger)
	if trig == nil {
		return "keep"
	}
	mins, ok := parseTriggerMinutes(trig.Value)
	if !ok || !alarmPresetMinutes[mins] {
		return "keep"
	}
	// The presets are "N minutes before the start". A trigger after the start, or one
	// counted from the end, is something else and is left as it is.
	before := strings.HasPrefix(strings.TrimSpace(trig.Value), "-") || mins == 0
	if !before || strings.EqualFold(trig.Params.Get("RELATED"), "END") {
		return "keep"
	}
	return strconv.Itoa(mins)
}

// parseTriggerMinutes parses a relative ISO-8601 TRIGGER duration into minutes
// (sign-agnostic). An absolute datetime value or garbage input returns ok=false.
// TRIGGER durations don't include months, so 'M' is always interpreted as minutes.
func parseTriggerMinutes(val string) (int, bool) {
	s := strings.TrimSpace(val)
	s = strings.TrimPrefix(s, "-")
	s = strings.TrimPrefix(s, "+")
	if len(s) == 0 || s[0] != 'P' {
		return 0, false
	}
	s = s[1:]
	total := 0
	num := ""
	for _, ch := range s {
		switch {
		case ch >= '0' && ch <= '9':
			num += string(ch)
		case ch == 'T':
			num = ""
		case ch == 'W':
			n, err := strconv.Atoi(num)
			if err != nil {
				return 0, false
			}
			total += n * 7 * 24 * 60
			num = ""
		case ch == 'D':
			n, err := strconv.Atoi(num)
			if err != nil {
				return 0, false
			}
			total += n * 24 * 60
			num = ""
		case ch == 'H':
			n, err := strconv.Atoi(num)
			if err != nil {
				return 0, false
			}
			total += n * 60
			num = ""
		case ch == 'M':
			n, err := strconv.Atoi(num)
			if err != nil {
				return 0, false
			}
			total += n
			num = ""
		case ch == 'S':
			n, err := strconv.Atoi(num)
			if err != nil {
				return 0, false
			}
			total += n / 60
			num = ""
		default:
			return 0, false
		}
	}
	return total, true
}
