package caldav_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	godavcaldav "github.com/emersion/go-webdav/caldav"
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
}

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
	pool, _ := pgxpool.New(ctx, dsn)
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
		case http.MethodPut:
			ctx = caldav.WithRawBody(ctx, raw)
		}
		gh.ServeHTTP(w, r.WithContext(ctx))
	})
	return &reviewEnv{h: h, pool: pool, svc: svc, ownerID: ownerID, sharee: db.UUIDString(sharee.ID),
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
