package dav

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/db"
)

// spanSlack widens an object's span on both sides. The span is computed from the wall
// clock of DTSTART/DTEND without resolving time zones (an object may carry its own
// VTIMEZONE, an all-day date is floating): every zone is within 14 hours of UTC, the
// viewer's too, and two days cover both with room to spare.
const spanSlack = 48 * time.Hour

// eventSpan returns the interval outside which no occurrence of the object's events can
// fall, for filtering a calendar by date in SQL. A nil bound is open: end is open for a
// recurring object, both are for one without events and for one that cannot be read
// with confidence — such an object is never filtered out. The span may be wider
// than the events; the exact check still runs on whatever passes the filter.
func eventSpan(data string) (start, end *time.Time) {
	cal, err := ical.NewDecoder(strings.NewReader(data)).Decode()
	if err != nil {
		return nil, nil
	}
	var first, last time.Time
	events, open := 0, false
	for _, comp := range cal.Children {
		if comp.Name != ical.CompEvent {
			continue
		}
		events++
		st, date, ok := wallClock(comp.Props.Get(ical.PropDateTimeStart))
		if !ok {
			return nil, nil
		}
		en := st
		if date {
			en = st.Add(24 * time.Hour)
		}
		if p := comp.Props.Get(ical.PropDateTimeEnd); p != nil {
			t, _, ok := wallClock(p)
			if !ok {
				return nil, nil
			}
			if t.After(st) {
				en = t
			}
		}
		if comp.Props.Get(ical.PropRecurrenceRule) != nil || comp.Props.Get(ical.PropRecurrenceDates) != nil ||
			comp.Props.Get(ical.PropDuration) != nil {
			open = true
		}
		if events == 1 || st.Before(first) {
			first = st
		}
		if events == 1 || en.After(last) {
			last = en
		}
	}
	if events == 0 {
		return nil, nil
	}
	first, last = first.Add(-spanSlack), last.Add(spanSlack)
	if open {
		return &first, nil
	}
	return &first, &last
}

// wallClock reads a DATE or DATE-TIME value as the clock time it states, in UTC, ignoring
// its zone; date reports a DATE value.
func wallClock(p *ical.Prop) (t time.Time, date, ok bool) {
	if p == nil {
		return time.Time{}, false, false
	}
	v := strings.TrimSuffix(strings.TrimSpace(p.Value), "Z")
	if t, err := time.Parse("20060102T150405", v); err == nil {
		return t, false, true
	}
	if t, err := time.Parse("20060102", v); err == nil {
		return t, true, true
	}
	return time.Time{}, false, false
}

func spanValue(t *time.Time) pgtype.Timestamptz {
	if t == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *t, Valid: true}
}

// CalendarObjectsInWindow returns the objects of a calendar that can have an occurrence
// between start and end: those whose span overlaps the window, the open-ended ones, and
// those not yet looked at. It is the listing for a date range — a month view reads
// the month's events, not the calendar's whole history.
func (s *Service) CalendarObjectsInWindow(ctx context.Context, calID string, start, end time.Time) ([]db.CalendarObject, error) {
	cid, err := db.ParseUUID(calID)
	if err != nil {
		return nil, err
	}
	return s.q.ListCalendarObjectsInWindow(ctx, db.ListCalendarObjectsInWindowParams{
		CalendarID:  cid,
		WindowStart: pgtype.Timestamptz{Time: start, Valid: true},
		WindowEnd:   pgtype.Timestamptz{Time: end, Valid: true},
	})
}

// BackfillSpans computes the span of the objects stored before spans existed. Until it has
// been through an object, that object is simply never filtered out. One pass over the
// table, in id order; it returns the number of objects it filled in.
func (s *Service) BackfillSpans(ctx context.Context) (int, error) {
	after := pgtype.UUID{Valid: true} // the zero id: below every row
	filled := 0
	for {
		rows, err := s.q.ListCalendarObjectsWithoutSpan(ctx, db.ListCalendarObjectsWithoutSpanParams{AfterID: after, MaxRows: 500})
		if err != nil {
			return filled, err
		}
		if len(rows) == 0 {
			return filled, nil
		}
		for _, r := range rows {
			after = r.ID
			// An object that cannot be read gets an open span and is settled too: it is
			// not parsed again at every start.
			start, end := eventSpan(r.Data)
			// Only if the object is still the one that was read: a write in between has
			// stored its own span.
			n, err := s.q.SetCalendarObjectSpan(ctx, db.SetCalendarObjectSpanParams{
				ID: r.ID, Etag: r.Etag, SpanStart: spanValue(start), SpanEnd: spanValue(end),
			})
			if err != nil {
				return filled, err
			}
			filled += int(n)
		}
	}
}

// BackfillSpansInBackground runs BackfillSpans once, logging what it did.
func (s *Service) BackfillSpansInBackground(ctx context.Context) {
	go func() {
		n, err := s.BackfillSpans(ctx)
		if err != nil {
			log.Printf("discodrive: calendar spans: %v", err)
			return
		}
		if n > 0 {
			log.Printf("discodrive: calendar spans: filled in %d objects", n)
		}
	}()
}
