package caldav_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	godavcaldav "github.com/emersion/go-webdav/caldav"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"discodrive/internal/caldav"
	"discodrive/internal/dav"
	"discodrive/internal/db"
)

type reviewEnv struct {
	h               http.Handler
	pool            *pgxpool.Pool
	svc             *dav.Service
	ownerID, sharee string
	calID, calDir   string
	queries         *queryCounter
}

// queryCounter counts the statements sent to the database.
type queryCounter struct {
	n   atomic.Int64
	mu  sync.Mutex
	sql []string
}

func (c *queryCounter) TraceQueryStart(ctx context.Context, _ *pgx.Conn, q pgx.TraceQueryStartData) context.Context {
	c.n.Add(1)
	c.mu.Lock()
	c.sql = append(c.sql, q.SQL)
	c.mu.Unlock()
	return ctx
}

// reset forgets what was counted; ran reports whether a statement with this sqlc name ran since.
func (c *queryCounter) reset() {
	c.n.Store(0)
	c.mu.Lock()
	c.sql = nil
	c.mu.Unlock()
}

func (c *queryCounter) ran(name string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, q := range c.sql {
		if strings.Contains(q, "-- name: "+name+" ") {
			return true
		}
	}
	return false
}
func (c *queryCounter) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// setupReview: two users, the owner's calendar, and a handler that dispatches like the
// middleware (PROPPATCH, PROPFIND, raw PUT body) with the user taken from X-User-ID.
func setupReview(t *testing.T) *reviewEnv {
	t.Helper()
	ctx := context.Background()
	pgC, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("kf"), tcpostgres.WithUsername("kf"), tcpostgres.WithPassword("kf"),
		tcpostgres.BasicWaitStrategies())
	if err != nil {
		t.Skipf("Docker required: %v", err)
	}
	t.Cleanup(func() { _ = pgC.Terminate(ctx) })
	dsn, _ := pgC.ConnectionString(ctx, "sslmode=disable")
	if err := db.MigrateUp(dsn); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	cfg, _ := pgxpool.ParseConfig(dsn)
	queries := &queryCounter{}
	cfg.ConnConfig.Tracer = queries
	pool, _ := pgxpool.NewWithConfig(ctx, cfg)
	t.Cleanup(pool.Close)
	q := db.New(pool)
	tenant, _ := q.CreateTenant(ctx, "t")
	owner, _ := q.CreateUser(ctx, db.CreateUserParams{TenantID: tenant.ID, Email: "owner@x", PasswordHash: "x", Role: "user"})
	sharee, _ := q.CreateUser(ctx, db.CreateUserParams{TenantID: tenant.ID, Email: "sharee@x", PasswordHash: "x", Role: "user"})
	svc := dav.NewService(pool)
	ownerID := db.UUIDString(owner.ID)
	cal, err := svc.CreateCalendar(ctx, ownerID, "Личный", "")
	if err != nil {
		t.Fatalf("CreateCalendar: %v", err)
	}
	backend := caldav.New(svc)
	gh := &godavcaldav.Handler{Backend: backend, Prefix: "/caldav"}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(raw))
		ctx := caldav.WithUserID(r.Context(), r.Header.Get("X-User-ID"))
		switch r.Method {
		case "PROPPATCH":
			backend.HandleProppatch(w, r.WithContext(ctx))
			return
		case "PROPFIND":
			backend.HandlePropfind(w, r.WithContext(ctx), gh)
			return
		case "REPORT":
			backend.HandleReport(w, r.WithContext(ctx), gh)
			return
		case http.MethodPut:
			ctx = caldav.WithRawBody(ctx, raw)
		}
		gh.ServeHTTP(w, r.WithContext(ctx))
	})
	return &reviewEnv{queries: queries, h: h, pool: pool, svc: svc, ownerID: ownerID, sharee: db.UUIDString(sharee.ID),
		calID: db.UUIDString(cal.ID), calDir: "/caldav/" + ownerID + "/cal/" + cal.Uri + "/"}
}

func (e *reviewEnv) req(t *testing.T, user, method, path, body string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("X-User-ID", user)
	if method == http.MethodPut {
		r.Header.Set("Content-Type", "text/calendar; charset=utf-8")
	} else if body != "" {
		r.Header.Set("Content-Type", "application/xml; charset=utf-8")
	}
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, r)
	return rec
}

func eventWith(uid, summary string) string {
	return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//t//EN\r\nBEGIN:VEVENT\r\nUID:" + uid +
		"\r\nDTSTAMP:20260610T000000Z\r\nDTSTART:20260612T120000Z\r\nDTEND:20260612T130000Z\r\nSUMMARY:" +
		summary + "\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
}

// RFC 4791 §5.3.2.1: a second resource with the UID of an existing one in the same calendar
// is refused with CALDAV:no-uid-conflict. Two copies of one event used to be stored.
func TestPutRefusesSecondObjectWithSameUID(t *testing.T) {
	e := setupReview(t)
	if rec := e.req(t, e.ownerID, http.MethodPut, e.calDir+"one.ics", eventWith("same-uid", "A"), nil); rec.Code >= 300 {
		t.Fatalf("first PUT: %d %s", rec.Code, rec.Body.String())
	}
	rec := e.req(t, e.ownerID, http.MethodPut, e.calDir+"two.ics", eventWith("same-uid", "B"), nil)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "no-uid-conflict") {
		t.Fatalf("second object with the same UID: %d %s, want 403 no-uid-conflict", rec.Code, rec.Body.String())
	}
	if get := e.req(t, e.ownerID, http.MethodGet, e.calDir+"two.ics", "", nil); get.Code != http.StatusNotFound {
		t.Fatalf("the duplicate was stored: GET %d", get.Code)
	}
	// updating the first object under its own name is still fine
	if rec := e.req(t, e.ownerID, http.MethodPut, e.calDir+"one.ics", eventWith("same-uid", "A2"), nil); rec.Code >= 300 {
		t.Fatalf("update in place: %d %s", rec.Code, rec.Body.String())
	}
	// a duplicate stored before the check existed stays writable in place (sync must not break)
	if _, err := e.pool.Exec(context.Background(),
		`INSERT INTO calendar_objects (calendar_id, uid, data, etag, parsed) VALUES ($1, 'legacy', $2, 'e', '{"uid":"same-uid"}')`,
		e.calID, eventWith("same-uid", "legacy")); err != nil {
		t.Fatal(err)
	}
	if rec := e.req(t, e.ownerID, http.MethodPut, e.calDir+"legacy.ics", eventWith("same-uid", "legacy 2"), nil); rec.Code >= 300 {
		t.Fatalf("update of a pre-existing duplicate: %d %s", rec.Code, rec.Body.String())
	}
}

// If-Match must hold at the moment of the write. With the calendar row locked, two writers
// holding the same etag both used to pass the check (made before the transaction) and both
// overwrote the event; now the second one sees the new etag and gets 412.
func TestIfMatchIsCheckedAtomically(t *testing.T) {
	e := setupReview(t)
	ctx := context.Background()
	put := e.req(t, e.ownerID, http.MethodPut, e.calDir+"x.ics", eventWith("x", "v0"), nil)
	if put.Code >= 300 {
		t.Fatalf("PUT: %d", put.Code)
	}
	etag := put.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag")
	}

	tx, err := e.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, "SELECT 1 FROM calendars WHERE id = $1 FOR UPDATE", e.calID); err != nil {
		t.Fatal(err)
	}
	codes := make([]int, 2)
	var wg sync.WaitGroup
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body := eventWith("x", "writer "+string(rune('a'+i)))
			codes[i] = e.req(t, e.ownerID, http.MethodPut, e.calDir+"x.ics", body, map[string]string{"If-Match": etag}).Code
		}(i)
	}
	time.Sleep(500 * time.Millisecond) // both writers are now waiting on the lock
	_ = tx.Rollback(ctx)
	wg.Wait()

	ok, failed := 0, 0
	for _, c := range codes {
		switch {
		case c < 300:
			ok++
		case c == http.StatusPreconditionFailed:
			failed++
		}
	}
	if ok != 1 || failed != 1 {
		t.Fatalf("two If-Match writers with the same etag: codes %v, want one success and one 412", codes)
	}
	// If-None-Match: * on an existing object is still refused
	if rec := e.req(t, e.ownerID, http.MethodPut, e.calDir+"x.ics", eventWith("x", "again"), map[string]string{"If-None-Match": "*"}); rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("If-None-Match * on an existing object: %d", rec.Code)
	}
}

// A sharee's PROPPATCH is not saved (the Set* queries are scoped to the owner): it must be
// answered 403 in the propstat, not acknowledged with 200.
func TestProppatchByShareeIsForbidden(t *testing.T) {
	e := setupReview(t)
	ctx := context.Background()
	if _, err := e.svc.ShareCalendar(ctx, e.ownerID, e.calID, "sharee@x", nil); err != nil {
		t.Fatalf("share: %v", err)
	}
	body := `<?xml version="1.0" encoding="UTF-8"?><D:propertyupdate xmlns:D="DAV:" xmlns:A="http://apple.com/ns/ical/"><D:set><D:prop><D:displayname>Renamed</D:displayname><A:calendar-color>#FF0000FF</A:calendar-color></D:prop></D:set></D:propertyupdate>`
	rec := e.req(t, e.sharee, "PROPPATCH", e.calDir, body, nil)
	if rec.Code != http.StatusMultiStatus || !strings.Contains(rec.Body.String(), "403 Forbidden") || strings.Contains(rec.Body.String(), "200 OK") {
		t.Fatalf("sharee PROPPATCH: %d %s, want 207 with a 403 propstat", rec.Code, rec.Body.String())
	}
	cal, _ := e.svc.GetCalendar(ctx, e.calID)
	if cal.Name != "Личный" || cal.Color != "" {
		t.Fatalf("sharee changed the calendar: name %q color %q", cal.Name, cal.Color)
	}
	// Apple reorders the sidebar by sending calendar-order (and color) for every calendar,
	// shared ones included: that must not come back as an error, and must not be stored.
	reorder := `<?xml version="1.0" encoding="UTF-8"?><D:propertyupdate xmlns:D="DAV:" xmlns:A="http://apple.com/ns/ical/"><D:set><D:prop><A:calendar-order>3</A:calendar-order><A:calendar-color>#00FF00FF</A:calendar-color></D:prop></D:set></D:propertyupdate>`
	rec = e.req(t, e.sharee, "PROPPATCH", e.calDir, reorder, nil)
	if rec.Code != http.StatusMultiStatus || !strings.Contains(rec.Body.String(), "200 OK") {
		t.Fatalf("sharee reorder: %d %s, want 207 with 200", rec.Code, rec.Body.String())
	}
	cal, _ = e.svc.GetCalendar(ctx, e.calID)
	if cal.Color != "" {
		t.Fatalf("sharee's color was stored on the owner's calendar: %q", cal.Color)
	}
	if rec := e.req(t, e.ownerID, "PROPPATCH", e.calDir, body, nil); !strings.Contains(rec.Body.String(), "200 OK") {
		t.Fatalf("owner PROPPATCH: %s", rec.Body.String())
	}
}

// A calendar-multiget (what a client sends after a sync report lists changed events) used
// to resolve the calendar and check access for every href. The number of queries must not
// grow with the number of events.
func TestMultigetQueriesDoNotGrowWithEvents(t *testing.T) {
	e := setupReview(t)
	const events = 40
	var body strings.Builder
	body.WriteString(`<?xml version="1.0" encoding="UTF-8"?><C:calendar-multiget xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav"><D:prop><D:getetag/><C:calendar-data/></D:prop>`)
	for i := 0; i < events; i++ {
		uid := fmt.Sprintf("ev-%02d", i)
		href := e.calDir + uid + ".ics"
		if rec := e.req(t, e.ownerID, http.MethodPut, href, eventWith(uid, "summary-"+uid), nil); rec.Code/100 != 2 {
			t.Fatalf("PUT %s: %d %s", uid, rec.Code, rec.Body)
		}
		body.WriteString("<D:href>" + href + "</D:href>")
	}
	body.WriteString("<D:href>" + e.calDir + "missing.ics</D:href></C:calendar-multiget>")

	e.queries.reset()
	rep := e.req(t, e.ownerID, "REPORT", e.calDir, body.String(), map[string]string{"Depth": "1"})
	if rep.Code != http.StatusMultiStatus {
		t.Fatalf("REPORT: %d %s", rep.Code, rep.Body)
	}
	used := e.queries.n.Load()
	out := rep.Body.String()
	for i := 0; i < events; i++ {
		if want := fmt.Sprintf("summary-ev-%02d", i); !strings.Contains(out, want) {
			t.Fatalf("the report misses %q", want)
		}
	}
	if !strings.Contains(out, "missing.ics") || !strings.Contains(out, "404") {
		t.Error("the event that does not exist is not reported as 404")
	}
	if used > 12 {
		t.Fatalf("a multiget of %d events made %d queries, want a constant handful", events, used)
	}
}

// A client checking what changed lists the calendar with ETags only (PROPFIND Depth:1).
// That must not read and parse every event.
func TestPropfindListingDoesNotLoadEvents(t *testing.T) {
	e := setupReview(t)
	for _, uid := range []string{"a", "b", "c"} {
		if rec := e.req(t, e.ownerID, http.MethodPut, e.calDir+uid+".ics", eventWith(uid, "secret-"+uid), nil); rec.Code/100 != 2 {
			t.Fatalf("PUT %s: %d %s", uid, rec.Code, rec.Body)
		}
	}
	etags := `<?xml version="1.0" encoding="UTF-8"?><D:propfind xmlns:D="DAV:"><D:prop><D:getetag/><D:getcontentlength/></D:prop></D:propfind>`
	e.queries.reset()
	rec := e.req(t, e.ownerID, "PROPFIND", e.calDir, etags, map[string]string{"Depth": "1"})
	if rec.Code != http.StatusMultiStatus {
		t.Fatalf("PROPFIND: %d %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	for _, uid := range []string{"a.ics", "b.ics", "c.ics"} {
		if !strings.Contains(body, uid) {
			t.Errorf("the listing misses %s", uid)
		}
	}
	if n := strings.Count(body, "getetag>"); n < 3 {
		t.Errorf("the listing carries %d getetag tags, want one per event", n)
	}
	if strings.Contains(body, "secret-") {
		t.Error("the listing carries event data nobody asked for")
	}
	if e.queries.ran("ListCalendarObjects") {
		t.Error("an ETag listing read every event in full")
	}

	// A client that does ask for the data in PROPFIND still gets it.
	withData := `<?xml version="1.0" encoding="UTF-8"?><D:propfind xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav"><D:prop><D:getetag/><C:calendar-data/></D:prop></D:propfind>`
	rec = e.req(t, e.ownerID, "PROPFIND", e.calDir, withData, map[string]string{"Depth": "1"})
	if rec.Code != http.StatusMultiStatus || !strings.Contains(rec.Body.String(), "secret-a") {
		t.Errorf("PROPFIND with calendar-data: %d, data present: %v", rec.Code, strings.Contains(rec.Body.String(), "secret-a"))
	}
}
