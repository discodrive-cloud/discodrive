package auth

import (
	"context"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/db"
)

type ctxKey int

const (
	ctxUserID ctxKey = iota
	ctxTenantID
	ctxRole
	ctxDeviceID
	ctxTokenVersion
	ctxSessionID
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
		who, err := s.requester(r.Context(), uid, claims)
		if err != nil {
			writeUnauthorized(w, "session is invalid")
			return
		}
		u := who.user
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
		if claims.DeviceID != "" && !who.deviceOK {
			writeUnauthorized(w, "device has been revoked")
			return
		}
		if claims.DeviceID == "" && !who.sessionOK {
			writeUnauthorized(w, "session has been revoked; sign in again")
			return
		}
		if who.sessionStale {
			// Best effort: a session whose use could not be noted is still in use.
			_ = s.q.TouchBrowserSession(r.Context(), claims.SessionID)
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
		if fresh, e := s.issuer.IssueTTL(claims.Subject, claims.TenantID, u.Role, u.TokenVersion, claims.DeviceID, ttl, claims.SessionID); e == nil {
			w.Header().Set("X-Token", fresh)
		}
		// Authed responses carry a per-request X-Token, so they must never be cached:
		// a cached response replays a stale X-Token, which the client (useApi) saves
		// over the live token — regressing the session to an expired one and forcing a
		// spurious 401/logout on the next navigation. no-store also keeps authenticated
		// data out of the browser cache.
		w.Header().Set("Cache-Control", "no-store")
		ctx := withIdentity(r.Context(), claims, u.Role)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// requester is what the middleware knows about who is asking.
type requester struct {
	user db.User
	// sessionOK: the token's browser session exists and was used recently enough.
	// sessionStale: it is time to note that it was used again.
	sessionOK, sessionStale bool
	// deviceOK: the token's device is still paired under this token version.
	deviceOK bool
}

// requester reads the user and the state of the token's session or device. With a
// database it is one query (two on every request, user then session, was a third of the
// work of serving a thumbnail); the seams a test sets instead answer the same questions.
func (s *Service) requester(ctx context.Context, uid pgtype.UUID, claims *Claims) (requester, error) {
	var did pgtype.UUID
	if claims.DeviceID != "" {
		var err error
		if did, err = db.ParseUUID(claims.DeviceID); err != nil {
			return requester{}, err
		}
	}
	if s.authRead != nil {
		row, err := s.authRead(ctx, db.AuthRequesterParams{UserID: uid, SessionID: claims.SessionID, DeviceID: did})
		if err != nil {
			return requester{}, err
		}
		return requester{user: row.User, sessionOK: claims.SessionID != "" && row.SessionOk,
			sessionStale: row.SessionStale, deviceOK: row.DeviceOk}, nil
	}
	u, err := s.lookupUser(ctx, uid)
	if err != nil {
		return requester{}, err
	}
	who := requester{user: u}
	if claims.DeviceID != "" {
		dev, err := s.q.GetDevice(ctx, did)
		who.deviceOK = err == nil && dev.UserID == uid && dev.TokenHash.Valid && dev.TokenVersion == claims.Ver
	} else {
		who.sessionOK = claims.SessionID != "" && s.sessionActive != nil && s.sessionActive(ctx, claims)
	}
	return who, nil
}

func withIdentity(ctx context.Context, claims *Claims, role string) context.Context {
	ctx = context.WithValue(ctx, ctxUserID, claims.Subject)
	ctx = context.WithValue(ctx, ctxTenantID, claims.TenantID)
	ctx = context.WithValue(ctx, ctxRole, role)
	ctx = context.WithValue(ctx, ctxDeviceID, claims.DeviceID)
	ctx = context.WithValue(ctx, ctxTokenVersion, claims.Ver)
	return context.WithValue(ctx, ctxSessionID, claims.SessionID)
}

// TicketParam is the query parameter a download ticket travels in.
const TicketParam = "dl"

// MiddlewareOrTicket is Middleware for a route a browser saves a file from. A browser
// following a link cannot send the Authorization header, and reading the file into the
// page to save it from there costs the file's size in memory. Such a GET may carry a
// ticket instead: minted for the signed-in session (DownloadTicket), good for this one
// path, dead with that session. The handler runs as that user, so its access checks are
// the ones a session request gets; the session is not renewed, the link is not a login.
func (s *Service) MiddlewareOrTicket(next http.Handler) http.Handler {
	session := s.Middleware(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ticket := r.URL.Query().Get(TicketParam)
		if ticket == "" || r.Method != http.MethodGet || r.Header.Get("Authorization") != "" {
			session.ServeHTTP(w, r)
			return
		}
		claims, u, err := s.validateScoped(r.Context(), ticket, purposeDownload, r.URL.Path)
		if err != nil {
			writeUnauthorized(w, "invalid download link")
			return
		}
		claims.TenantID = db.UUIDString(u.TenantID)
		w.Header().Set("Cache-Control", "private, no-store")
		// The link must not travel on: not to another site, not into a shared cache.
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r.WithContext(withIdentity(r.Context(), claims, u.Role)))
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
		(r.Method == http.MethodPut && p == "/me/password") ||
		(r.Method == http.MethodPost && p == "/auth/logout")
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
