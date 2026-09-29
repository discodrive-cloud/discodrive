package api

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/teambition/rrule-go"
)

// windowsZones maps the Windows zone names Outlook/Exchange write as TZID to IANA names
// (CLDR windowsZones, territory 001). Only the names seen in practice; anything else falls
// back to the object's VTIMEZONE.
var windowsZones = map[string]string{
	"Dateline Standard Time":          "Etc/GMT+12",
	"UTC-11":                          "Etc/GMT+11",
	"Aleutian Standard Time":          "America/Adak",
	"Hawaiian Standard Time":          "Pacific/Honolulu",
	"Alaskan Standard Time":           "America/Anchorage",
	"Pacific Standard Time (Mexico)":  "America/Tijuana",
	"Pacific Standard Time":           "America/Los_Angeles",
	"US Mountain Standard Time":       "America/Phoenix",
	"Mountain Standard Time (Mexico)": "America/Mazatlan",
	"Mountain Standard Time":          "America/Denver",
	"Central America Standard Time":   "America/Guatemala",
	"Central Standard Time":           "America/Chicago",
	"Central Standard Time (Mexico)":  "America/Mexico_City",
	"Canada Central Standard Time":    "America/Regina",
	"SA Pacific Standard Time":        "America/Bogota",
	"Eastern Standard Time":           "America/New_York",
	"Eastern Standard Time (Mexico)":  "America/Cancun",
	"US Eastern Standard Time":        "America/Indiana/Indianapolis",
	"Venezuela Standard Time":         "America/Caracas",
	"Paraguay Standard Time":          "America/Asuncion",
	"Atlantic Standard Time":          "America/Halifax",
	"Central Brazilian Standard Time": "America/Cuiaba",
	"SA Western Standard Time":        "America/La_Paz",
	"Pacific SA Standard Time":        "America/Santiago",
	"Newfoundland Standard Time":      "America/St_Johns",
	"E. South America Standard Time":  "America/Sao_Paulo",
	"Argentina Standard Time":         "America/Argentina/Buenos_Aires",
	"SA Eastern Standard Time":        "America/Cayenne",
	"Montevideo Standard Time":        "America/Montevideo",
	"UTC-02":                          "Etc/GMT+2",
	"Azores Standard Time":            "Atlantic/Azores",
	"Cape Verde Standard Time":        "Atlantic/Cape_Verde",
	"UTC":                             "Etc/UTC",
	"Coordinated Universal Time":      "Etc/UTC",
	"GMT Standard Time":               "Europe/London",
	"Greenwich Standard Time":         "Atlantic/Reykjavik",
	"W. Europe Standard Time":         "Europe/Berlin",
	"Central Europe Standard Time":    "Europe/Budapest",
	"Romance Standard Time":           "Europe/Paris",
	"Central European Standard Time":  "Europe/Warsaw",
	"W. Central Africa Standard Time": "Africa/Lagos",
	"Morocco Standard Time":           "Africa/Casablanca",
	"Jordan Standard Time":            "Asia/Amman",
	"GTB Standard Time":               "Europe/Bucharest",
	"Middle East Standard Time":       "Asia/Beirut",
	"Egypt Standard Time":             "Africa/Cairo",
	"E. Europe Standard Time":         "Europe/Chisinau",
	"Syria Standard Time":             "Asia/Damascus",
	"South Africa Standard Time":      "Africa/Johannesburg",
	"FLE Standard Time":               "Europe/Kiev",
	"Israel Standard Time":            "Asia/Jerusalem",
	"Kaliningrad Standard Time":       "Europe/Kaliningrad",
	"Namibia Standard Time":           "Africa/Windhoek",
	"Arabic Standard Time":            "Asia/Baghdad",
	"Turkey Standard Time":            "Europe/Istanbul",
	"Arab Standard Time":              "Asia/Riyadh",
	"Belarus Standard Time":           "Europe/Minsk",
	"Russian Standard Time":           "Europe/Moscow",
	"E. Africa Standard Time":         "Africa/Nairobi",
	"Iran Standard Time":              "Asia/Tehran",
	"Arabian Standard Time":           "Asia/Dubai",
	"Azerbaijan Standard Time":        "Asia/Baku",
	"Russia Time Zone 3":              "Europe/Samara",
	"Mauritius Standard Time":         "Indian/Mauritius",
	"Georgian Standard Time":          "Asia/Tbilisi",
	"Caucasus Standard Time":          "Asia/Yerevan",
	"Afghanistan Standard Time":       "Asia/Kabul",
	"West Asia Standard Time":         "Asia/Tashkent",
	"Ekaterinburg Standard Time":      "Asia/Yekaterinburg",
	"Pakistan Standard Time":          "Asia/Karachi",
	"India Standard Time":             "Asia/Kolkata",
	"Sri Lanka Standard Time":         "Asia/Colombo",
	"Nepal Standard Time":             "Asia/Kathmandu",
	"Central Asia Standard Time":      "Asia/Bishkek",
	"Bangladesh Standard Time":        "Asia/Dhaka",
	"Omsk Standard Time":              "Asia/Omsk",
	"Myanmar Standard Time":           "Asia/Yangon",
	"SE Asia Standard Time":           "Asia/Bangkok",
	"N. Central Asia Standard Time":   "Asia/Novosibirsk",
	"North Asia Standard Time":        "Asia/Krasnoyarsk",
	"China Standard Time":             "Asia/Shanghai",
	"North Asia East Standard Time":   "Asia/Irkutsk",
	"Singapore Standard Time":         "Asia/Singapore",
	"W. Australia Standard Time":      "Australia/Perth",
	"Taipei Standard Time":            "Asia/Taipei",
	"Ulaanbaatar Standard Time":       "Asia/Ulaanbaatar",
	"Tokyo Standard Time":             "Asia/Tokyo",
	"Korea Standard Time":             "Asia/Seoul",
	"Yakutsk Standard Time":           "Asia/Yakutsk",
	"Cen. Australia Standard Time":    "Australia/Adelaide",
	"AUS Central Standard Time":       "Australia/Darwin",
	"E. Australia Standard Time":      "Australia/Brisbane",
	"AUS Eastern Standard Time":       "Australia/Sydney",
	"West Pacific Standard Time":      "Pacific/Port_Moresby",
	"Tasmania Standard Time":          "Australia/Hobart",
	"Vladivostok Standard Time":       "Asia/Vladivostok",
	"Magadan Standard Time":           "Asia/Magadan",
	"Russia Time Zone 10":             "Asia/Srednekolymsk",
	"Russia Time Zone 11":             "Asia/Kamchatka",
	"Central Pacific Standard Time":   "Pacific/Guadalcanal",
	"New Zealand Standard Time":       "Pacific/Auckland",
	"UTC+12":                          "Etc/GMT-12",
	"Fiji Standard Time":              "Pacific/Fiji",
	"Tonga Standard Time":             "Pacific/Tongatapu",
	"Samoa Standard Time":             "Pacific/Apia",
	"Line Islands Standard Time":      "Pacific/Kiritimati",
}

// loadZone loads an IANA zone by name. "Local" and "" are refused: the first is the
// server's zone, the second silently means UTC — neither is a zone a client named.
func loadZone(name string) (*time.Location, bool) {
	if name == "" || name == "Local" {
		return nil, false
	}
	switch name {
	case "UTC", "Etc/UTC", "Etc/UCT", "UCT", "Etc/GMT", "GMT", "Etc/Universal", "Universal", "Etc/Zulu", "Zulu":
		return time.UTC, true // written as Z, not as TZID=Etc/UTC
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, false
	}
	return loc, true
}

// resolveTZName turns a TZID into a loadable zone: an IANA name as is, a Windows name
// through windowsZones, and the prefixed forms older Lightning/Evolution wrote
// ("/mozilla.org/20050126_1/Europe/Berlin") by their trailing IANA part.
func resolveTZName(tzid string) (*time.Location, bool) {
	tzid = strings.TrimSpace(strings.Trim(tzid, `"`))
	if loc, ok := loadZone(tzid); ok {
		return loc, true
	}
	if name, ok := windowsZones[tzid]; ok {
		return loadZone(name)
	}
	if strings.HasPrefix(tzid, "/") {
		parts := strings.Split(strings.Trim(tzid, "/"), "/")
		for n := 3; n >= 2; n-- {
			if len(parts) > n {
				if loc, ok := loadZone(strings.Join(parts[len(parts)-n:], "/")); ok {
					return loc, true
				}
			}
		}
	}
	return nil, false
}

// timeProps are the date-time properties read by the calendar and task handlers.
var timeProps = []string{ical.PropDateTimeStart, ical.PropDateTimeEnd, ical.PropDue,
	ical.PropRecurrenceID, ical.PropExceptionDates, ical.PropRecurrenceDates}

// normalizeTimes rewrites, in memory, the date-time properties go-ical cannot read so that
// an event is not dropped from the list (or read as an empty form and saved back as year 1):
//   - a TZID that is not an IANA name is replaced by the mapped IANA name; failing that the
//     value is converted to UTC with the offsets of the object's own VTIMEZONE; failing that
//     the TZID is dropped and the time is read as floating;
//   - a comma-separated EXDATE/RDATE list is split into one property per value.
//
// It is meant for a freshly decoded copy that is read, or whose rewritten properties are
// replaced before it is stored.
func normalizeTimes(cal *ical.Calendar) {
	vtz := map[string]*ical.Component{}
	for _, c := range cal.Children {
		if c.Name == ical.CompTimezone {
			if id := c.Props.Get(ical.PropTimezoneID); id != nil {
				vtz[id.Value] = c
			}
		}
	}
	for _, comp := range cal.Children {
		if comp.Name != ical.CompEvent && comp.Name != ical.CompToDo {
			continue
		}
		for _, name := range timeProps {
			props := comp.Props[name]
			if len(props) == 0 {
				continue
			}
			var out []ical.Prop
			for _, p := range props {
				values := []string{p.Value}
				if strings.Contains(p.Value, ",") {
					values = strings.Split(p.Value, ",")
				}
				for _, v := range values {
					np := p
					np.Params = cloneParams(p.Params)
					np.Value = strings.TrimSpace(v)
					fixTZID(&np, vtz)
					out = append(out, np)
				}
			}
			comp.Props[name] = out
		}
	}
}

func cloneParams(p ical.Params) ical.Params {
	out := make(ical.Params, len(p))
	for k, v := range p {
		out[k] = append([]string(nil), v...)
	}
	return out
}

func fixTZID(p *ical.Prop, vtz map[string]*ical.Component) {
	tzid := p.Params.Get(ical.PropTimezoneID)
	if tzid == "" {
		return
	}
	if _, err := time.LoadLocation(tzid); err == nil {
		return
	}
	if loc, ok := resolveTZName(tzid); ok {
		p.Params.Set(ical.PropTimezoneID, loc.String())
		return
	}
	p.Params.Del(ical.PropTimezoneID)
	if len(p.Value) != len("20060102T150405") {
		return // a DATE, or already UTC: nothing to convert
	}
	wall, err := time.ParseInLocation("20060102T150405", p.Value, time.UTC)
	if err != nil {
		return
	}
	if tz := vtz[tzid]; tz != nil {
		if off, ok := vtimezoneOffset(tz, wall); ok {
			p.Value = wall.Add(-time.Duration(off) * time.Second).Format("20060102T150405Z")
		}
	}
	// no VTIMEZONE: the TZID is gone and the value stays floating
}

// vtimezoneOffset returns the UTC offset (seconds) a VTIMEZONE defines for a wall-clock
// time: the offset of the observance (STANDARD/DAYLIGHT) whose last onset is the latest one
// not after that time. wall carries the local time in the UTC location.
func vtimezoneOffset(tz *ical.Component, wall time.Time) (int, bool) {
	var best time.Time
	bestOff, found := 0, false
	var earliest time.Time
	earliestFrom, anyObs := 0, false
	for _, obs := range tz.Children {
		if obs.Name != ical.CompTimezoneStandard && obs.Name != ical.CompTimezoneDaylight {
			continue
		}
		to, okTo := parseUTCOffset(propText(obs, ical.PropTimezoneOffsetTo))
		from, okFrom := parseUTCOffset(propText(obs, ical.PropTimezoneOffsetFrom))
		st := obs.Props.Get(ical.PropDateTimeStart)
		if !okTo || st == nil {
			continue
		}
		start, err := time.ParseInLocation("20060102T150405", st.Value, time.UTC)
		if err != nil {
			continue
		}
		if !okFrom {
			from = to
		}
		if !anyObs || start.Before(earliest) {
			earliest, earliestFrom, anyObs = start, from, true
		}
		onset := time.Time{}
		if !start.After(wall) {
			onset = start
		}
		if rr := obs.Props.Get(ical.PropRecurrenceRule); rr != nil {
			if opt, err := rrule.StrToROptionInLocation(rr.Value, time.UTC); err == nil {
				// Outlook anchors its rules in 1601; rrule-go gives up iterating long before
				// today from there. The yearly BYMONTH/BYDAY rules do not depend on the
				// anchor's year, so start counting two years before the time looked up.
				opt.Dtstart = start
				if y := wall.Year() - 2; start.Year() < y {
					opt.Dtstart = time.Date(y, start.Month(), start.Day(), start.Hour(), start.Minute(), start.Second(), 0, time.UTC)
				}
				if r, err := rrule.NewRRule(*opt); err == nil {
					if t := r.Before(wall, true); !t.IsZero() && t.After(onset) {
						onset = t
					}
				}
			}
		}
		for _, rd := range obs.Props[ical.PropRecurrenceDates] {
			for _, v := range strings.Split(rd.Value, ",") {
				if t, err := time.ParseInLocation("20060102T150405", strings.TrimSpace(v), time.UTC); err == nil && !t.After(wall) && t.After(onset) {
					onset = t
				}
			}
		}
		if !onset.IsZero() && (!found || onset.After(best)) {
			best, bestOff, found = onset, to, true
		}
	}
	if found {
		return bestOff, true
	}
	if anyObs {
		return earliestFrom, true // before the first onset: the offset it changes from
	}
	return 0, false
}

// parseUTCOffset parses a UTC-OFFSET value (+HHMM or +HHMMSS) into seconds.
func parseUTCOffset(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if len(s) != 5 && len(s) != 7 {
		return 0, false
	}
	sign := 1
	switch s[0] {
	case '+':
	case '-':
		sign = -1
	default:
		return 0, false
	}
	h, e1 := strconv.Atoi(s[1:3])
	m, e2 := strconv.Atoi(s[3:5])
	sec := 0
	var e3 error
	if len(s) == 7 {
		sec, e3 = strconv.Atoi(s[5:7])
	}
	if e1 != nil || e2 != nil || e3 != nil {
		return 0, false
	}
	return sign * (h*3600 + m*60 + sec), true
}

func formatUTCOffset(sec int) string {
	sign := '+'
	if sec < 0 {
		sign, sec = '-', -sec
	}
	s := fmt.Sprintf("%c%02d%02d", sign, sec/3600, sec%3600/60)
	if sec%60 != 0 {
		s += fmt.Sprintf("%02d", sec%60)
	}
	return s
}

// ensureVTimezones adds a VTIMEZONE for every IANA TZID the calendar's events and tasks
// reference but do not define (RFC 5545 §3.2.19 requires one per TZID). A TZID that already
// has a VTIMEZONE keeps it untouched.
func ensureVTimezones(cal *ical.Calendar) {
	have := map[string]bool{}
	for _, c := range cal.Children {
		if c.Name == ical.CompTimezone {
			if id := c.Props.Get(ical.PropTimezoneID); id != nil {
				have[id.Value] = true
			}
		}
	}
	var add []*ical.Component
	for _, comp := range cal.Children {
		if comp.Name != ical.CompEvent && comp.Name != ical.CompToDo {
			continue
		}
		for _, name := range timeProps {
			for _, p := range comp.Props[name] {
				tzid := p.Params.Get(ical.PropTimezoneID)
				if tzid == "" || have[tzid] {
					continue
				}
				loc, ok := loadZone(tzid)
				if !ok {
					continue
				}
				year := time.Now().Year()
				if t, err := time.ParseInLocation("20060102T150405", p.Value, loc); err == nil {
					year = t.Year()
				}
				have[tzid] = true
				add = append(add, buildVTimezone(tzid, loc, year))
			}
		}
	}
	if len(add) > 0 {
		cal.Children = append(add, cal.Children...)
	}
}

// buildVTimezone describes a zone from Go's tz database: the offset changes of the given
// year become yearly STANDARD/DAYLIGHT rules (BYMONTH + nth or last weekday), anchored in
// 1970 so that every date of the series is covered. A zone without changes that year gets a
// single STANDARD observance.
func buildVTimezone(tzid string, loc *time.Location, year int) *ical.Component {
	tz := ical.NewComponent(ical.CompTimezone)
	tz.Props.SetText(ical.PropTimezoneID, tzid)

	type change struct {
		at       time.Time
		from, to int
		name     string
	}
	var changes []change
	t := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(year+1, 1, 1, 0, 0, 0, 0, time.UTC)
	_, prev := t.In(loc).Zone()
	for t.Before(end) {
		next := t.Add(time.Hour)
		if _, off := next.In(loc).Zone(); off != prev {
			lo, hi := t, next // the change happens in (lo, hi]
			for hi.Sub(lo) > time.Second {
				mid := lo.Add(hi.Sub(lo) / 2)
				if _, o := mid.In(loc).Zone(); o == prev {
					lo = mid
				} else {
					hi = mid
				}
			}
			name, _ := hi.In(loc).Zone()
			changes = append(changes, change{at: hi, from: prev, to: off, name: name})
			prev = off
		}
		t = next
	}

	observance := func(kind string, from, to int, name string, dtstart time.Time, rule string) {
		o := ical.NewComponent(kind)
		o.Props.Set(rawProp(ical.PropTimezoneOffsetFrom, formatUTCOffset(from)))
		o.Props.Set(rawProp(ical.PropTimezoneOffsetTo, formatUTCOffset(to)))
		if name != "" {
			o.Props.SetText(ical.PropTimezoneName, name)
		}
		o.Props.Set(rawProp(ical.PropDateTimeStart, dtstart.Format("20060102T150405")))
		if rule != "" {
			o.Props.Set(rawProp(ical.PropRecurrenceRule, rule))
		}
		tz.Children = append(tz.Children, o)
	}

	if len(changes) == 0 {
		name, off := time.Date(year, 7, 1, 0, 0, 0, 0, time.UTC).In(loc).Zone()
		observance(ical.CompTimezoneStandard, off, off, name, time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC), "")
		return tz
	}
	minOff := changes[0].from
	for _, c := range changes {
		if c.to < minOff {
			minOff = c.to
		}
		if c.from < minOff {
			minOff = c.from
		}
	}
	weekdays := []string{"SU", "MO", "TU", "WE", "TH", "FR", "SA"}
	for _, c := range changes {
		// DTSTART and the rule are in the local time in force before the change.
		wall := c.at.In(time.FixedZone("", c.from))
		wall = time.Date(wall.Year(), wall.Month(), wall.Day(), wall.Hour(), wall.Minute(), wall.Second(), 0, time.UTC)
		n := (wall.Day()-1)/7 + 1
		if wall.Day()+7 > daysIn(wall.Month(), wall.Year()) {
			n = -1
		}
		rule := fmt.Sprintf("FREQ=YEARLY;BYMONTH=%d;BYDAY=%d%s", int(wall.Month()), n, weekdays[wall.Weekday()])
		anchor := nthWeekday(1970, wall.Month(), wall.Weekday(), n)
		anchor = time.Date(1970, anchor.Month(), anchor.Day(), wall.Hour(), wall.Minute(), wall.Second(), 0, time.UTC)
		kind := ical.CompTimezoneStandard
		if c.to > minOff {
			kind = ical.CompTimezoneDaylight
		}
		observance(kind, c.from, c.to, c.name, anchor, rule)
	}
	return tz
}

func rawProp(name, value string) *ical.Prop {
	p := ical.NewProp(name)
	p.Value = value
	return p
}

func daysIn(m time.Month, year int) int {
	return time.Date(year, m+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// nthWeekday returns the n-th (1..5, or -1 for the last) weekday wd of the month.
func nthWeekday(year int, m time.Month, wd time.Weekday, n int) time.Time {
	if n < 0 {
		d := time.Date(year, m, daysIn(m, year), 0, 0, 0, 0, time.UTC)
		for d.Weekday() != wd {
			d = d.AddDate(0, 0, -1)
		}
		return d
	}
	d := time.Date(year, m, 1, 0, 0, 0, 0, time.UTC)
	for d.Weekday() != wd {
		d = d.AddDate(0, 0, 1)
	}
	return d.AddDate(0, 0, 7*(n-1))
}
