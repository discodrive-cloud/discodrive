package auth

import (
	"context"
	"net/http"
	"strings"

	"discodrive/internal/db"
)

type ctxKey int

const (
	ctxUserID ctxKey = iota
	ctxTenantID
	ctxRole
	ctxDeviceID
	ctxTokenVersion
)

// Middleware requires a valid Bearer JWT and injects user_id/tenant_id/role into the context.
// Every protected request is scoped to these values.
func (s *Service) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || token == "" {
			writeUnauthorized(w, "authorization required")
			return
		}
		claims, err := s.issuer.Parse(token)
		if err != nil {
			writeUnauthorized(w, "invalid token")
			return
		}
		// An MFA-pending token (purpose=mfa) proves only the password step — it must not
		// reach any protected route. Reject before touching the DB.
		if claims.Pur != "" {
			writeUnauthorized(w, "complete sign-in")
			return
		}
		// Role and user existence are verified against the DB on every request rather than
		// trusted from the token: otherwise a demotion (admin→user) or account deletion
		// would not affect active sessions — the old token would keep carrying role=admin,
		// and sliding renewal would re-issue it indefinitely.
		uid, err := db.ParseUUID(claims.Subject)
		if err != nil {
			writeUnauthorized(w, "invalid token")
			return
		}
		u, err := s.lookupUser(r.Context(), uid)
		if err != nil {
			writeUnauthorized(w, "session is invalid")
			return
		}
		// Token version must match the DB: a password change increments the counter,
		// making all previously issued tokens (including stolen ones) return 401.
		if claims.Ver != u.TokenVersion {
			writeUnauthorized(w, "session is invalid")
			return
		}
		// Admin-provisioned account with a temporary password: locked out of everything
		// (403) except reading the profile and submitting the new password, until changed.
		// Default-deny by allowlist so a forgotten route can't leak through. (A.2)
		if u.MustChangePassword && !passwordChangeExempt(r) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"password change required","code":"must_change_password"}` + "\n"))
			return
		}
		// A device token (sync daemon) is valid only as long as the device itself is alive:
		// removing it in the admin panel causes the daemon's very next request to get 401
		// (instant revocation, same logic as demotion/account deletion above).
		if claims.DeviceID != "" {
			did, derr := db.ParseUUID(claims.DeviceID)
			if derr != nil {
				writeUnauthorized(w, "session is invalid")
				return
			}
			dev, derr := s.q.GetDevice(r.Context(), did)
			if derr != nil || db.UUIDString(dev.UserID) != claims.Subject || !dev.TokenHash.Valid || dev.TokenVersion != claims.Ver {
				writeUnauthorized(w, "device has been revoked")
				return
			}
		}
		// Sliding session: on every authorized request we renew the token
		// (new exp = now+TTL) and return it in X-Token; the client saves it. Idle
		// longer than TTL → token expires → must log in again. We preserve device_id,
		// otherwise renewal would silently drop the device binding. Header is set before the response body.
		//
		// The TTL is the user's own session setting, read from the row we just loaded, so
		// changing it in settings takes effect on the very next request. Device tokens
		// (sync daemon) keep the server default: that session is not the browser's.
		ttl := s.issuer.ttl
		if claims.DeviceID == "" {
			ttl = SessionTTL(u.SessionTtlMinutes)
		}
		if fresh, e := s.issuer.IssueTTL(claims.Subject, claims.TenantID, u.Role, u.TokenVersion, claims.DeviceID, ttl); e == nil {
			w.Header().Set("X-Token", fresh)
		}
		// Authed responses carry a per-request X-Token, so they must never be cached:
		// a cached response replays a stale X-Token, which the client (useApi) saves
		// over the live token — regressing the session to an expired one and forcing a
		// spurious 401/logout on the next navigation. no-store also keeps authenticated
		// data out of the browser cache.
		w.Header().Set("Cache-Control", "no-store")
		ctx := context.WithValue(r.Context(), ctxUserID, claims.Subject)
		ctx = context.WithValue(ctx, ctxTenantID, claims.TenantID)
		ctx = context.WithValue(ctx, ctxRole, u.Role)
		ctx = context.WithValue(ctx, ctxDeviceID, claims.DeviceID)
		ctx = context.WithValue(ctx, ctxTokenVersion, claims.Ver)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequireAdmin allows only admins through (must be placed AFTER Middleware).
func (s *Service) RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if Role(r.Context()) != "admin" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"admin privileges required"}` + "\n"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// UserID/TenantID/Role read values from the authenticated context.
func UserID(ctx context.Context) string   { v, _ := ctx.Value(ctxUserID).(string); return v }
func TenantID(ctx context.Context) string { v, _ := ctx.Value(ctxTenantID).(string); return v }
func Role(ctx context.Context) string     { v, _ := ctx.Value(ctxRole).(string); return v }

// passwordChangeExempt reports whether a request is allowed while the user must still
// change their password: reading the profile and submitting the new password.
func passwordChangeExempt(r *http.Request) bool {
	p := r.URL.Path
	return (r.Method == http.MethodGet && p == "/me") ||
		(r.Method == http.MethodPut && p == "/me/password")
}

func writeUnauthorized(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"error":"` + msg + `"}` + "\n"))
}

// DeviceID and TokenVersion preserve the authority of the session that began a request.
func DeviceID(ctx context.Context) string { v, _ := ctx.Value(ctxDeviceID).(string); return v }
func TokenVersion(ctx context.Context) (int64, bool) {
	v, ok := ctx.Value(ctxTokenVersion).(int64)
	return v, ok
}
