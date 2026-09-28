package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeDAV records what reached it and answers like the CalDAV/CardDAV handlers would.
type fakeDAV struct {
	name       string
	path, body string
	dav        string
}

func (f *fakeDAV) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.path = r.URL.Path
	b, _ := io.ReadAll(r.Body)
	f.body = string(b)
	w.Header().Set("DAV", f.dav)
	w.WriteHeader(http.StatusMultiStatus)
}

// Apple accounts from our profile carry the bare host as HostName and periodically re-discover
// from "/" (OPTIONS + PROPFIND Depth 0). A 405 there shows up as "not a location that supports
// this request" in Calendar.
func TestDAVRootRoutesToCalDAVOrCardDAV(t *testing.T) {
	const calBody = `<A:propfind xmlns:A="DAV:"><A:prop><A:current-user-principal/><A:principal-URL/></A:prop></A:propfind>`
	const cardBody = `<A:propfind xmlns:A="DAV:"><A:prop><A:current-user-principal/><B:addressbook-home-set xmlns:B="urn:ietf:params:xml:ns:carddav"/></A:prop></A:propfind>`
	for _, tc := range []struct {
		name, method, body, wantTarget, wantPath string
	}{
		{"caldav propfind", "PROPFIND", calBody, "cal", "/"},
		{"carddav propfind", "PROPFIND", cardBody, "card", "/"},
		{"options", http.MethodOptions, "", "cal", "/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cal := &fakeDAV{name: "cal", dav: "1, 3, calendar-access"}
			card := &fakeDAV{name: "card", dav: "1, 3, addressbook"}
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(tc.method, "/", strings.NewReader(tc.body))
			davRoot(cal, card).ServeHTTP(rec, req)

			got, other := cal, card
			if tc.wantTarget == "card" {
				got, other = card, cal
			}
			if got.path != tc.wantPath || other.path != "" {
				t.Fatalf("routed to cal=%q card=%q, want %s at %q", cal.path, card.path, tc.wantTarget, tc.wantPath)
			}
			if got.body != tc.body {
				t.Fatalf("body was not forwarded: %q", got.body)
			}
			if rec.Code != http.StatusMultiStatus {
				t.Fatalf("code=%d", rec.Code)
			}
		})
	}
}

func TestDAVRootOptionsAdvertisesBothWhenEnabled(t *testing.T) {
	cal := &fakeDAV{dav: "1, 3, calendar-access"}
	rec := httptest.NewRecorder()
	davRoot(cal, &fakeDAV{}).ServeHTTP(rec, httptest.NewRequest(http.MethodOptions, "/", nil))
	if dav := rec.Header().Get("DAV"); !strings.Contains(dav, "calendar-access") || !strings.Contains(dav, "addressbook") {
		t.Fatalf("DAV header %q must advertise calendar-access and addressbook", dav)
	}
}

func TestDAVRootWithOnlyCardDAV(t *testing.T) {
	card := &fakeDAV{dav: "1, 3, addressbook"}
	rec := httptest.NewRecorder()
	davRoot(nil, card).ServeHTTP(rec, httptest.NewRequest("PROPFIND", "/", strings.NewReader("<propfind xmlns=\"DAV:\"/>")))
	if card.path != "/" {
		t.Fatalf("with CalDAV off, root must go to CardDAV, got %q", card.path)
	}
}
