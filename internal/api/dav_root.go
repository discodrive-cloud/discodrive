package api

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
)

// davRoot answers OPTIONS/PROPFIND on "/". Apple accounts from our profile carry the bare host
// as HostName, and dataaccessd periodically re-discovers the account from "/"; a 405 there shows
// up as "… is not a location that supports this request" in Calendar. The request is passed to
// the CalDAV handler (or the CardDAV one when the body asks for CardDAV properties), which after
// auth answers "/" as the user's principal: Apple reads calendar-home-set straight from it.
//
// enabled reports the admin's caldav.enabled / carddav.enabled flags: a protocol switched off
// answers 403 "disabled", so "/" goes to the one that is on (a CardDAV-only server used to send
// every root request to the disabled CalDAV handler).
func davRoot(caldavH, carddavH http.Handler, enabled func(ctx context.Context, key string) bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		calOn := caldavH != nil && enabled(r.Context(), "caldav.enabled")
		cardOn := carddavH != nil && enabled(r.Context(), "carddav.enabled")
		wantsCard := bytes.Contains(body, []byte("urn:ietf:params:xml:ns:carddav"))
		var target http.Handler
		switch {
		case wantsCard && cardOn, !calOn && cardOn:
			target = carddavH
		case calOn:
			target = caldavH
		case caldavH != nil:
			target = caldavH // both off: the handler answers "disabled"
		default:
			target = carddavH
		}
		if target == nil {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		r2 := r.Clone(r.Context())
		r2.Body = io.NopCloser(bytes.NewReader(body))
		if r.Method == http.MethodOptions && calOn && cardOn {
			w = &davHeaderWriter{ResponseWriter: w}
		}
		target.ServeHTTP(w, r2)
	})
}

// settingOn reports whether a boolean admin setting is "true".
func (s *Server) settingOn(ctx context.Context, key string) bool {
	return s.getSettingValue(ctx, key) == "true"
}

// davHeaderWriter makes OPTIONS on "/" advertise both calendar-access and addressbook, since
// the root is shared by the CalDAV and CardDAV accounts.
type davHeaderWriter struct {
	http.ResponseWriter
	done bool
}

func (d *davHeaderWriter) WriteHeader(code int) {
	if !d.done {
		d.done = true
		h := d.Header()
		for _, c := range []string{"calendar-access", "addressbook"} {
			if dav := h.Get("DAV"); !strings.Contains(dav, c) {
				h.Set("DAV", strings.TrimPrefix(dav+", "+c, ", "))
			}
		}
	}
	d.ResponseWriter.WriteHeader(code)
}

func (d *davHeaderWriter) Write(p []byte) (int, error) {
	if !d.done {
		d.WriteHeader(http.StatusOK)
	}
	return d.ResponseWriter.Write(p)
}
