package caldav

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"

	"github.com/beevik/etree"

	"discodrive/internal/dav"
	"discodrive/internal/db"
)

// reportMemo holds what one REPORT has already read. go-webdav answers a multiget by
// asking the backend for every href separately; without the memo each object cost a
// calendar lookup, an access check and its own read. A REPORT changes nothing, so within
// it nothing goes stale.
type reportMemo struct {
	calendars map[string]resolvedCalendar // by collection uri
	// objects holds, per calendar id, the objects fetched ahead for a multiget; a uid
	// missing from a fetched calendar does not exist.
	objects map[string]map[string]dav.StoredObject
}

type resolvedCalendar struct {
	cal db.Calendar
	err error
}

type reportMemoKey struct{}

func reportMemoOf(ctx context.Context) *reportMemo {
	m, _ := ctx.Value(reportMemoKey{}).(*reportMemo)
	return m
}

// HandleReport serves REPORT through go-webdav with one memo for the whole request and
// the objects of a calendar-multiget read ahead in one query per calendar.
func (b *Backend) HandleReport(w http.ResponseWriter, r *http.Request, next http.Handler) {
	ctx := context.WithValue(r.Context(), reportMemoKey{}, &reportMemo{
		calendars: map[string]resolvedCalendar{}, objects: map[string]map[string]dav.StoredObject{},
	})
	if raw, err := io.ReadAll(r.Body); err == nil {
		r.Body = io.NopCloser(bytes.NewReader(raw))
		b.prefetchMultiget(ctx, raw)
	}
	next.ServeHTTP(w, r.WithContext(ctx))
}

// object returns a calendar object's data and ETag, from the memo when the REPORT fetched
// it ahead.
func (b *Backend) object(ctx context.Context, calID, uid string) (data, etag string, err error) {
	if m := reportMemoOf(ctx); m != nil {
		if objs, fetched := m.objects[calID]; fetched {
			if o, ok := objs[uid]; ok {
				return o.Data, o.ETag, nil
			}
			return "", "", dav.ErrNotFound
		}
	}
	return b.svc.GetCalendarObject(ctx, calID, uid)
}

// prefetchMultiget reads the objects a calendar-multiget names. Anything it cannot make
// sense of is left to the per-href path.
func (b *Backend) prefetchMultiget(ctx context.Context, body []byte) {
	m := reportMemoOf(ctx)
	if m == nil || len(body) == 0 {
		return
	}
	doc := etree.NewDocument()
	if err := doc.ReadFromBytes(body); err != nil {
		return
	}
	root := doc.Root()
	if root == nil || root.Tag != "calendar-multiget" {
		return
	}
	uids := map[string][]string{} // by collection uri
	for _, href := range root.SelectElements("href") {
		u, err := url.Parse(href.Text())
		if err != nil {
			continue
		}
		if _, uri, obj := parsePath(u.Path); uri != "" && obj != "" {
			uids[uri] = append(uids[uri], obj)
		}
	}
	for uri, list := range uids {
		cal, err := b.resolveCalendar(ctx, uri)
		if err != nil {
			continue
		}
		id := db.UUIDString(cal.ID)
		if objs, err := b.svc.CalendarObjectsByUIDs(ctx, id, list); err == nil {
			m.objects[id] = objs
		}
	}
}
