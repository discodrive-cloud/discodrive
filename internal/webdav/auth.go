package webdav

import (
	"context"
	"errors"
	"net/http"

	"discodrive/internal/auth"
	"discodrive/internal/db"
)

// ctxUserKey / ctxDeviceKey are defined in fs.go (same package).

// SettingsReader retrieves a setting value (e.g. the webdav.enabled flag).
type SettingsReader interface {
	GetSetting(ctx context.Context, key string) (db.Setting, error)
}

// Auth implements HTTP Basic auth over app-specific passwords. On success it stores
// userID/deviceID in the context. Checks the global webdav.enabled flag.
func Auth(authSvc *auth.Service, settings SettingsReader, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if v, err := settings.GetSetting(r.Context(), "webdav.enabled"); err != nil || v.Value != "true" {
			http.Error(w, "WebDAV is disabled", http.StatusForbidden)
			return
		}
		userID, deviceID, err := authSvc.AuthenticateDAV(r)
		if errors.Is(err, auth.ErrDAVBusy) {
			w.Header().Set("Retry-After", "60")
			http.Error(w, "authentication temporarily limited; retry later", http.StatusTooManyRequests)
			return
		}
		if err != nil {
			unauthorized(w)
			return
		}

		ctx := context.WithValue(r.Context(), ctxUserKey, userID)
		ctx = context.WithValue(ctx, ctxDeviceKey, deviceID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Basic realm="DiscoDrive"`)
	http.Error(w, "authorization required", http.StatusUnauthorized)
}

// UserID retrieves the authenticated user ID from the context.
func UserID(ctx context.Context) string {
	if v, ok := ctx.Value(ctxUserKey).(string); ok {
		return v
	}
	return ""
}
