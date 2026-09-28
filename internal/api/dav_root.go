package api

import (
	"bytes"
	"io"
	"net/http"
	"strings"
)

// davRoot answers OPTIONS/PROPFIND on "/". Apple accounts from our profile carry the bare host
// as HostName, and dataaccessd periodically re-discovers the account from "/"; a 405 there shows
// up as "… is not a location that supports this request" in Calendar. The request is served by
// the CalDAV handler as if it came to /caldav/ (auth, enable flag and current-user-principal
// live there), or by the CardDAV handler when the body asks for CardDAV properties.
func davRoot(caldavH, carddavH http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		target, path := caldavH, "/caldav/"
		if caldavH == nil || bytes.Contains(body, []byte("urn:ietf:params:xml:ns:carddav")) {
			target, path = carddavH, "/carddav/"
		}
		if target == nil {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		r2 := r.Clone(r.Context())
		r2.URL.Path, r2.URL.RawPath, r2.RequestURI = path, "", path
		r2.Body = io.NopCloser(bytes.NewReader(body))
		if r.Method == http.MethodOptions && caldavH != nil && carddavH != nil {
			w = &davHeaderWriter{ResponseWriter: w}
		}
		target.ServeHTTP(w, r2)
	})
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
