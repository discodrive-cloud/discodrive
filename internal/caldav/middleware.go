package caldav

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"

	"discodrive/internal/auth"
	"discodrive/internal/db"
	"discodrive/internal/httpsecurity"
)

const maxBody = 1 << 20 // 1 MB per object

// SettingsReader returns a setting value (e.g. the caldav.enabled flag).
type SettingsReader interface {
	GetSetting(ctx context.Context, key string) (db.Setting, error)
}

// Handler wraps the caldav.Handler with a middleware chain: enable-flag → Basic-auth → raw-body.
// backend is needed for methods that go-webdav does not dispatch (MKCALENDAR).
func Handler(authSvc *auth.Service, settings SettingsReader, backend *Backend, dav http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if v, err := settings.GetSetting(r.Context(), "caldav.enabled"); err != nil || v.Value != "true" {
			http.Error(w, "CalDAV disabled", http.StatusForbidden)
			return
		}
		uid, _, err := authSvc.AuthenticateDAV(r)
		if errors.Is(err, auth.ErrDAVBusy) {
			w.Header().Set("Retry-After", "60")
			http.Error(w, "authentication temporarily limited; retry later", http.StatusTooManyRequests)
			return
		}
		if err != nil {
			unauthorized(w)
			return
		}

		raw, ok := httpsecurity.ReadDAVBody(w, r, maxBody)
		if !ok {
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		ctx := WithUserID(r.Context(), uid)
		// PROPPATCH is not implemented by go-webdav (returns 501), which breaks Apple clients.
		// We handle it ourselves with a no-op 207 response (see proppatch.go).
		if r.Method == "PROPPATCH" {
			backend.HandleProppatch(w, r.WithContext(ctx))
			return
		}
		// MKCALENDAR is not dispatched by go-webdav (405) — this is how Apple Reminders
		// creates a VTODO collection. We create it ourselves (see mkcalendar.go).
		if r.Method == "MKCALENDAR" {
			backend.HandleMkcalendar(w, r.WithContext(ctx))
			return
		}
		// PROPFIND: pass through go-webdav and inject calendar-color (see propfind_augment.go).
		if r.Method == "PROPFIND" {
			backend.HandlePropfind(w, r.WithContext(ctx), dav)
			return
		}
		if r.Method == http.MethodPut {
			ctx = WithRawBody(ctx, raw)
		}

		dav.ServeHTTP(w, r.WithContext(ctx))
	})
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Basic realm="DiscoDrive"`)
	http.Error(w, "authorization required", http.StatusUnauthorized)
}

// WellKnown redirects /.well-known/caldav to /caldav/ (discovery).
func WellKnown() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, prefix+"/", http.StatusMovedPermanently)
	})
}
