package caldav_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	godavcaldav "github.com/emersion/go-webdav/caldav"
	"github.com/jackc/pgx/v5/pgxpool"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"discodrive/internal/caldav"
	"discodrive/internal/dav"
	"discodrive/internal/db"
)

const evt = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//t//EN\r\nBEGIN:VEVENT\r\nUID:e1\r\nSUMMARY:Встреча\r\nDTSTAMP:20260610T000000Z\r\nDTSTART:20260612T120000Z\r\nDTEND:20260612T130000Z\r\nX-APPLE-TRAVEL:keepme\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"

func setup(t *testing.T) (http.Handler, string, string) {
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
	u, _ := q.CreateUser(ctx, db.CreateUserParams{TenantID: tenant.ID, Email: "u@x", PasswordHash: "x", Role: "user"})
	userID := db.UUIDString(u.ID)
	svc := dav.NewService(pool)
	cal, _ := svc.CreateCalendar(ctx, userID, "Личный", "")

	backend := caldav.New(svc)
	h := &godavcaldav.Handler{Backend: backend, Prefix: "/caldav"}
	wrapped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := caldav.WithUserID(r.Context(), userID)
		switch r.Method {
		case "PROPPATCH":
			backend.HandleProppatch(w, r.WithContext(c))
			return
		case "PROPFIND":
			backend.HandlePropfind(w, r.WithContext(c), h)
			return
		}
		if r.Method == http.MethodPut {
			raw, _ := io.ReadAll(r.Body)
			c = caldav.WithRawBody(c, raw)
			r.Body = io.NopCloser(bytes.NewReader(raw))
		}
		h.ServeHTTP(w, r.WithContext(c))
	})
	return wrapped, userID, cal.Uri
}

func do(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if method == http.MethodPut {
		req.Header.Set("Content-Type", "text/calendar; charset=utf-8")
	} else if body != "" {
		req.Header.Set("Content-Type", "application/xml; charset=utf-8")
	} else if body != "" {
		req.Header.Set("Content-Type", "application/xml; charset=utf-8")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestPutGetRoundTrip(t *testing.T) {
	h, userID, uri := setup(t)
	objPath := "/caldav/" + userID + "/cal/" + uri + "/e1.ics"

	put := do(t, h, http.MethodPut, objPath, evt)
	if put.Code != http.StatusCreated && put.Code != http.StatusNoContent && put.Code != http.StatusOK {
		t.Fatalf("PUT code=%d body=%s", put.Code, put.Body.String())
	}
	if put.Header().Get("ETag") == "" {
		t.Fatalf("PUT did not return an ETag")
	}

	get := do(t, h, http.MethodGet, objPath, "")
	if get.Code != http.StatusOK {
		t.Fatalf("GET code=%d body=%s", get.Code, get.Body.String())
	}
	for _, want := range []string{"UID:e1", "SUMMARY:Встреча", "X-APPLE-TRAVEL:keepme"} {
		if !strings.Contains(get.Body.String(), want) {
			t.Fatalf("GET output is missing %q:\n%s", want, get.Body.String())
		}
	}
}

func TestDeleteThen404(t *testing.T) {
	h, userID, uri := setup(t)
	objPath := "/caldav/" + userID + "/cal/" + uri + "/e1.ics"
	do(t, h, http.MethodPut, objPath, evt)
	del := do(t, h, http.MethodDelete, objPath, "")
	if del.Code != http.StatusNoContent && del.Code != http.StatusOK {
		t.Fatalf("DELETE code=%d", del.Code)
	}
	get := do(t, h, http.MethodGet, objPath, "")
	if get.Code != http.StatusNotFound {
		t.Fatalf("expected 404 after delete, got %d", get.Code)
	}
}

// setupTwo spins up a test CalDAV server with two users (owner and grantee).
// Returns a handler with per-request auth (X-User-ID header), dav.Service, ownerID, granteeID.
func setupTwo(t *testing.T) (http.Handler, *dav.Service, string, string) {
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
	grantee, _ := q.CreateUser(ctx, db.CreateUserParams{TenantID: tenant.ID, Email: "grantee@x", PasswordHash: "x", Role: "user"})
	ownerID := db.UUIDString(owner.ID)
	granteeID := db.UUIDString(grantee.ID)
	svc := dav.NewService(pool)

	h := &godavcaldav.Handler{Backend: caldav.New(svc), Prefix: "/caldav"}
	// handler reads X-User-ID from the request for test auth
	wrapped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uid := r.Header.Get("X-User-ID")
		c := caldav.WithUserID(r.Context(), uid)
		if r.Method == http.MethodPut {
			raw, _ := io.ReadAll(r.Body)
			c = caldav.WithRawBody(c, raw)
			r.Body = io.NopCloser(bytes.NewReader(raw))
		}
		h.ServeHTTP(w, r.WithContext(c))
	})
	return wrapped, svc, ownerID, granteeID
}

// doAs sends a request on behalf of the specified user.
func doAs(t *testing.T, h http.Handler, uid, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("X-User-ID", uid)
	if method == http.MethodPut {
		req.Header.Set("Content-Type", "text/calendar; charset=utf-8")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestSharedCalendarVisibleAndWritable(t *testing.T) {
	ctx := context.Background()
	h, svc, ownerID, granteeID := setupTwo(t)

	// owner: create calendar and add an event
	cal, err := svc.CreateCalendar(ctx, ownerID, "Семейный", "")
	if err != nil {
		t.Fatalf("CreateCalendar: %v", err)
	}
	calURI := cal.Uri
	calID := db.UUIDString(cal.ID)

	ownerObjPath := "/caldav/" + ownerID + "/cal/" + calURI + "/e1.ics"
	put := doAs(t, h, ownerID, http.MethodPut, ownerObjPath, evt)
	if put.Code != http.StatusCreated && put.Code != http.StatusNoContent && put.Code != http.StatusOK {
		t.Fatalf("owner PUT code=%d body=%s", put.Code, put.Body.String())
	}

	// share: owner shares with grantee
	if _, err := svc.ShareCalendar(ctx, ownerID, calID, "grantee@x", nil); err != nil {
		t.Fatalf("ShareCalendar: %v", err)
	}

	// PROPFIND grantee's home-set at Depth:1 — must include the owner's URI
	propfindBody := `<?xml version="1.0" encoding="UTF-8"?><propfind xmlns="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav"><prop><C:calendar-description/><displayname/><resourcetype/></prop></propfind>`
	homeSetReq := httptest.NewRequest("PROPFIND", "/caldav/"+granteeID+"/cal/", strings.NewReader(propfindBody))
	homeSetReq.Header.Set("X-User-ID", granteeID)
	homeSetReq.Header.Set("Depth", "1")
	homeSetReq.Header.Set("Content-Type", "application/xml")
	homeSetRec := httptest.NewRecorder()
	h.ServeHTTP(homeSetRec, homeSetReq)
	if homeSetRec.Code != http.StatusMultiStatus {
		t.Fatalf("PROPFIND home-set code=%d body=%s", homeSetRec.Code, homeSetRec.Body.String())
	}
	if !strings.Contains(homeSetRec.Body.String(), calURI) {
		t.Fatalf("PROPFIND does not contain owner uri %q:\n%s", calURI, homeSetRec.Body.String())
	}

	// GET the object as grantee — path is under the owner's namespace
	// (toDAVCalendar builds the path with uid=ownerID, so grantee accesses /caldav/{ownerID}/cal/{uri}/{obj})
	getObjPath := "/caldav/" + ownerID + "/cal/" + calURI + "/e1.ics"
	getRec := doAs(t, h, granteeID, http.MethodGet, getObjPath, "")
	if getRec.Code != http.StatusOK {
		t.Fatalf("grantee GET code=%d body=%s", getRec.Code, getRec.Body.String())
	}

	// PUT a new object as grantee → 201/204/200
	newObj2 := strings.ReplaceAll(evt, "UID:e1", "UID:e2")
	newObjPath := "/caldav/" + ownerID + "/cal/" + calURI + "/e2.ics"
	putRec := doAs(t, h, granteeID, http.MethodPut, newObjPath, newObj2)
	if putRec.Code != http.StatusCreated && putRec.Code != http.StatusNoContent && putRec.Code != http.StatusOK {
		t.Fatalf("grantee PUT code=%d body=%s", putRec.Code, putRec.Body.String())
	}
}

const propfindColor = `<?xml version="1.0" encoding="UTF-8"?>
<propfind xmlns="DAV:" xmlns:A="http://apple.com/ns/ical/"><prop><displayname/><A:calendar-color/></prop></propfind>`

func TestProppatchPersistsCalendarColor(t *testing.T) {
	h, userID, uri := setup(t)
	calPath := "/caldav/" + userID + "/cal/" + uri + "/"

	before := do(t, h, "PROPFIND", calPath, propfindColor)
	if strings.Contains(before.Body.String(), "#") {
		t.Fatalf("no color was set, but PROPFIND returned one:\n%s", before.Body.String())
	}

	pp := do(t, h, "PROPPATCH", calPath, `<?xml version="1.0" encoding="UTF-8"?>
<A:propertyupdate xmlns:A="DAV:"><A:set><A:prop><B:calendar-color xmlns:B="http://apple.com/ns/ical/" symbolic-color="custom">#1BADF8FF</B:calendar-color></A:prop></A:set></A:propertyupdate>`)
	if pp.Code != http.StatusMultiStatus {
		t.Fatalf("PROPPATCH code=%d body=%s", pp.Code, pp.Body.String())
	}

	after := do(t, h, "PROPFIND", calPath, propfindColor)
	if after.Code != http.StatusMultiStatus {
		t.Fatalf("PROPFIND code=%d body=%s", after.Code, after.Body.String())
	}
	body := after.Body.String()
	if !strings.Contains(body, "#1badf8FF") || !strings.Contains(body, "http://apple.com/ns/ical/") {
		t.Fatalf("PROPFIND did not return the saved calendar-color:\n%s", body)
	}
}
