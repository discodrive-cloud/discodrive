package dav

import (
	"github.com/emersion/go-ical"
	"github.com/teambition/rrule-go"
)

// Limits for expanding a stored RRULE with rrule-go, which cannot be cancelled and
// materializes whatever the rule asks for: a whole period's worth of times (every
// BYHOUR×BYMINUTE×BYSECOND combination on every day) before the first occurrence comes
// out, and every occurrence from DTSTART on, however far back DTSTART lies.
const (
	maxRuleTimes    = 96     // times per day: every 15 minutes around the clock
	maxSubDailyRuns = 100000 // COUNT a SECONDLY/MINUTELY rule must stay within
)

// MaxRecurringPerObject is how many recurring events (with an RRULE, not overrides) one
// stored object may hold and still be expanded. Each is one rrule-go run that cannot be
// cancelled — a rule that never matches runs to year 9999 — and one object can hold any
// number of events with the same UID. Real objects hold one.
const MaxRecurringPerObject = 20

// BoundedRecurrence reports whether every RRULE of comp can be expanded at a bounded
// cost: at most maxRuleTimes times per day, and a SECONDLY or MINUTELY rule only with
// a COUNT that ends it. A component without a rule, or with one that does not parse,
// is bounded: there is nothing to expand.
func BoundedRecurrence(comp *ical.Component) bool {
	opt, err := comp.Props.RecurrenceRule()
	if err != nil || opt == nil {
		return true
	}
	n := func(l []int) int { return max(len(l), 1) }
	times := 1
	switch opt.Freq {
	case rrule.SECONDLY:
	case rrule.MINUTELY:
		times = n(opt.Bysecond)
	case rrule.HOURLY: // per day: every hour, or the BYHOUR ones
		hours := 24
		if len(opt.Byhour) > 0 {
			hours = len(opt.Byhour)
		}
		times = hours * n(opt.Byminute) * n(opt.Bysecond)
	default:
		times = n(opt.Byhour) * n(opt.Byminute) * n(opt.Bysecond)
	}
	if times > maxRuleTimes {
		return false
	}
	if opt.Freq == rrule.SECONDLY || opt.Freq == rrule.MINUTELY {
		return opt.Count > 0 && opt.Count <= maxSubDailyRuns
	}
	return true
}

// CalendarBounded reports whether every event and to-do in cal is BoundedRecurrence and
// the object holds at most MaxRecurringPerObject recurring ones.
func CalendarBounded(cal *ical.Calendar) bool {
	recurring := 0
	for _, c := range cal.Children {
		if c.Name != ical.CompEvent && c.Name != ical.CompToDo {
			continue
		}
		if !BoundedRecurrence(c) {
			return false
		}
		if c.Props.Get(ical.PropRecurrenceRule) != nil {
			if recurring++; recurring > MaxRecurringPerObject {
				return false
			}
		}
	}
	return true
}

// HasRecurrence reports whether any event or to-do in cal repeats (RRULE or RDATE).
func HasRecurrence(cal *ical.Calendar) bool {
	for _, c := range cal.Children {
		if (c.Name == ical.CompEvent || c.Name == ical.CompToDo) &&
			(c.Props.Get(ical.PropRecurrenceRule) != nil || c.Props.Get(ical.PropRecurrenceDates) != nil) {
			return true
		}
	}
	return false
}
