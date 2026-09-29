package caldav

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/caldav"

	"discodrive/internal/dav"
	"discodrive/internal/db"
)

type ctxKey int

const ctxUserKey ctxKey = 0

// WithUserID stores the userID in the context (called by the auth middleware).
func WithUserID(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, ctxUserKey, userID)
}

func userID(ctx context.Context) string {
	if v, ok := ctx.Value(ctxUserKey).(string); ok {
		return v
	}
	return ""
}

// Backend implements caldav.Backend on top of dav.Service.
type Backend struct {
	svc *dav.Service
}

func New(svc *dav.Service) *Backend { return &Backend{svc: svc} }

func notFound() error { return webdav.NewHTTPError(404, errors.New("not found")) }

func (b *Backend) CurrentUserPrincipal(ctx context.Context) (string, error) {
	return principalPath(userID(ctx)), nil
}

func (b *Backend) CalendarHomeSetPath(ctx context.Context) (string, error) {
	return homeSetPath(userID(ctx)), nil
}

func (b *Backend) ListCalendars(ctx context.Context) ([]caldav.Calendar, error) {
	uid := userID(ctx)
	cals, err := b.svc.ListCalendars(ctx, uid)
	if err != nil {
		return nil, err
	}
	// Auto-provision: the client (Apple/Thunderbird) must have at least one collection on discovery.
	if len(cals) == 0 {
		def, derr := b.svc.EnsureDefaultCalendar(ctx, uid)
		if derr != nil {
			return nil, derr
		}
		cals = []db.Calendar{def}
	}
	out := make([]caldav.Calendar, 0, len(cals))
	for _, c := range cals {
		out = append(out, toDAVCalendar(uid, c))
	}
	if shared, serr := b.svc.SharedCalendarsForUser(ctx, uid); serr == nil {
		for _, sc := range shared {
			out = append(out, toDAVCalendar(uid, sc.Calendar))
		}
	}
	return out, nil
}

func (b *Backend) GetCalendar(ctx context.Context, path string) (*caldav.Calendar, error) {
	uid := userID(ctx)
	_, uri, _ := parsePath(path)
	c, err := b.svc.CalendarByURI(ctx, uri)
	if errors.Is(err, dav.ErrNotFound) {
		return nil, notFound()
	}
	if err != nil {
		return nil, err
	}
	if ok, _ := b.svc.CanAccessCalendar(ctx, uid, db.UUIDString(c.ID)); !ok {
		return nil, notFound()
	}
	cal := toDAVCalendar(uid, c)
	return &cal, nil
}

func (b *Backend) CreateCalendar(ctx context.Context, cal *caldav.Calendar) error {
	_, err := b.svc.CreateCalendar(ctx, userID(ctx), cal.Name, "")
	return err
}

func toDAVCalendar(uid string, c db.Calendar) caldav.Calendar {
	comps := strings.Split(c.Components, ",")
	for i := range comps {
		comps[i] = strings.TrimSpace(comps[i])
	}
	return caldav.Calendar{
		Path:                  calendarPath(uid, c.Uri),
		Name:                  c.Name,
		SupportedComponentSet: comps,
	}
}

// resolveCalendar looks up the calendar by URI and checks access (owner or share).
func (b *Backend) resolveCalendar(ctx context.Context, uri string) (db.Calendar, error) {
	c, err := b.svc.CalendarByURI(ctx, uri)
	if errors.Is(err, dav.ErrNotFound) {
		return db.Calendar{}, notFound()
	}
	if err != nil {
		return db.Calendar{}, err
	}
	if ok, _ := b.svc.CanAccessCalendar(ctx, userID(ctx), db.UUIDString(c.ID)); !ok {
		return db.Calendar{}, notFound()
	}
	return c, nil
}

func decodeICal(data string) (*ical.Calendar, error) {
	return ical.NewDecoder(strings.NewReader(data)).Decode()
}

func (b *Backend) GetCalendarObject(ctx context.Context, path string, req *caldav.CalendarCompRequest) (*caldav.CalendarObject, error) {
	uid := userID(ctx)
	_, uri, obj := parsePath(path)
	cal, err := b.resolveCalendar(ctx, uri)
	if err != nil {
		return nil, err
	}
	data, etag, err := b.svc.GetCalendarObject(ctx, db.UUIDString(cal.ID), obj)
	if errors.Is(err, dav.ErrNotFound) {
		return nil, notFound()
	}
	if err != nil {
		return nil, err
	}
	ico, err := decodeICal(data)
	if err != nil {
		return nil, err
	}
	return &caldav.CalendarObject{
		Path: objectPath(uid, uri, obj), ETag: etag,
		ContentLength: int64(len(data)), Data: ico,
	}, nil
}

func (b *Backend) ListCalendarObjects(ctx context.Context, path string, req *caldav.CalendarCompRequest) ([]caldav.CalendarObject, error) {
	uid := userID(ctx)
	_, uri, _ := parsePath(path)
	cal, err := b.resolveCalendar(ctx, uri)
	if err != nil {
		return nil, err
	}
	objs, err := b.svc.ListCalendarObjects(ctx, db.UUIDString(cal.ID))
	if err != nil {
		return nil, err
	}
	out := make([]caldav.CalendarObject, 0, len(objs))
	for _, o := range objs {
		ico, derr := decodeICal(o.Data)
		if derr != nil {
			continue
		}
		out = append(out, caldav.CalendarObject{
			Path: objectPath(uid, uri, o.Uid), ETag: o.Etag,
			ModTime: o.UpdatedAt.Time, ContentLength: int64(len(o.Data)), Data: ico,
		})
	}
	return out, nil
}

func (b *Backend) QueryCalendarObjects(ctx context.Context, path string, query *caldav.CalendarQuery) ([]caldav.CalendarObject, error) {
	objs, err := b.ListCalendarObjects(ctx, path, nil)
	if err != nil {
		return nil, err
	}
	return caldav.Filter(query, objs)
}

func (b *Backend) PutCalendarObject(ctx context.Context, path string, calData *ical.Calendar, opts *caldav.PutCalendarObjectOptions) (*caldav.CalendarObject, error) {
	uid := userID(ctx)
	_, uri, obj := parsePath(path)
	cal, err := b.resolveCalendar(ctx, uri)
	if err != nil {
		return nil, err
	}
	calID := db.UUIDString(cal.ID)

	raw := rawBody(ctx)
	if len(raw) == 0 {
		var buf bytes.Buffer
		if err := ical.NewEncoder(&buf).Encode(calData); err != nil {
			return nil, err
		}
		raw = buf.Bytes()
	}
	// If-Match / If-None-Match are checked by the service inside the write's transaction:
	// checked here, before it, a second writer could land in between and be overwritten.
	etag, err := b.svc.PutCalendarObjectIf(ctx, calID, obj, string(raw), putCheck(opts))
	switch {
	case errors.Is(err, dav.ErrPrecondition):
		return nil, webdav.NewHTTPError(http.StatusPreconditionFailed, err)
	case errors.Is(err, dav.ErrUIDConflict):
		// RFC 4791 §5.3.2.1: CALDAV:no-uid-conflict, 403
		return nil, webdav.NewHTTPError(http.StatusForbidden,
			errors.Unwrap(caldav.NewPreconditionError(caldav.PreconditionNoUIDConflict)))
	case err != nil:
		return nil, err
	}
	return &caldav.CalendarObject{Path: objectPath(uid, uri, obj), ETag: etag}, nil
}

// putCheck turns the conditional headers of a PUT into a dav.PutCheck (nil: none set).
func putCheck(opts *caldav.PutCalendarObjectOptions) dav.PutCheck {
	if opts == nil || (!opts.IfNoneMatch.IsSet() && !opts.IfMatch.IsSet()) {
		return nil
	}
	return func(exists bool, etag string) bool {
		if opts.IfNoneMatch.IsWildcard() && exists {
			return false
		}
		if opts.IfMatch.IsSet() {
			ok, _ := opts.IfMatch.MatchETag(etag)
			return exists && ok
		}
		return true
	}
}

func (b *Backend) DeleteCalendarObject(ctx context.Context, path string) error {
	_, uri, obj := parsePath(path)
	cal, err := b.resolveCalendar(ctx, uri)
	if err != nil {
		return err
	}
	err = b.svc.DeleteCalendarObject(ctx, db.UUIDString(cal.ID), obj)
	if errors.Is(err, dav.ErrNotFound) {
		return notFound()
	}
	return err
}

var _ caldav.Backend = (*Backend)(nil)
