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
	case rrule.HOURLY:
		times = n(opt.Byminute) * n(opt.Bysecond)
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

// CalendarBounded reports whether every event and to-do in cal is BoundedRecurrence.
func CalendarBounded(cal *ical.Calendar) bool {
	for _, c := range cal.Children {
		if (c.Name == ical.CompEvent || c.Name == ical.CompToDo) && !BoundedRecurrence(c) {
			return false
		}
	}
	return true
}
