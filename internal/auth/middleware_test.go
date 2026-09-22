package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/db"
)

// validUUID is a well-formed UUID for the token subject (middleware parses it into pgtype.UUID).
const validUUID = "11111111-1111-1111-1111-111111111111"

// Sliding session: a valid request renews the token (X-Token is fresh and valid);
// no token → 401 and no X-Token. Role is read from the DB (lookupUser stub).
func TestMiddleware_SlidingToken(t *testing.T) {
	iss := NewTokenIssuer("secret", time.Hour)
	uid, _ := db.ParseUUID(validUUID)
	svc := &Service{issuer: iss, lookupUser: func(context.Context, pgtype.UUID) (db.User, error) {
		return db.User{ID: uid, Role: "user"}, nil
	}}
	tok, err := iss.Issue(validUUID, "t1", "user", 0, "")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	h := svc.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if UserID(r.Context()) != validUUID {
			t.Errorf("user_id not set in context")
		}
		w.WriteHeader(http.StatusOK)
	}))

	// valid token → 200 + fresh valid X-Token
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, expected 200", rec.Code)
	}
	fresh := rec.Header().Get("X-Token")
	if fresh == "" {
		t.Fatal("X-Token not set")
	}
	if _, err := iss.Parse(fresh); err != nil {
		t.Fatalf("X-Token is invalid: %v", err)
	}

	// no token → 401, no X-Token
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rec2.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec2.Code)
	}
	if rec2.Header().Get("X-Token") != "" {
		t.Fatal("X-Token must not be set when there is no authorization")
	}
}

// Deleted/unavailable user: token is valid, but DB lookup fails → 401.
// Ensures that deleting an account closes the active session.
func TestMiddleware_DeletedUserRejected(t *testing.T) {
	iss := NewTokenIssuer("secret", time.Hour)
	svc := &Service{issuer: iss, lookupUser: func(context.Context, pgtype.UUID) (db.User, error) {
		return db.User{}, errors.New("no such user")
	}}
	tok, _ := iss.Issue(validUUID, "t1", "admin", 0, "")

	h := svc.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("handler must not be called for a deleted user")
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for deleted user, got %d", rec.Code)
	}
}

// Role comes from the DB, not the token: token was issued with role=admin, but the DB
// returns user → RequireAdmin must reject the request (demotion takes effect immediately).
func TestMiddleware_RoleFromDBNotToken(t *testing.T) {
	iss := NewTokenIssuer("secret", time.Hour)
	uid, _ := db.ParseUUID(validUUID)
	svc := &Service{issuer: iss, lookupUser: func(context.Context, pgtype.UUID) (db.User, error) {
		return db.User{ID: uid, Role: "user"}, nil // already demoted in DB
	}}
	tok, _ := iss.Issue(validUUID, "t1", "admin", 0, "") // token still carries admin

	var gotRole string
	h := svc.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRole = Role(r.Context())
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	h.ServeHTTP(httptest.NewRecorder(), req)
	if gotRole != "user" {
		t.Fatalf("role in context=%q, expected user (from DB, not token)", gotRole)
	}
}

// An MFA-pending token (purpose=mfa) must never reach a protected route: it only proves
// the password step, not a completed login. Middleware rejects it with 401 before any DB lookup.
func TestMiddleware_MFATokenRejected(t *testing.T) {
	iss := NewTokenIssuer("secret", time.Hour)
	svc := &Service{issuer: iss, lookupUser: func(context.Context, pgtype.UUID) (db.User, error) {
		t.Error("lookupUser must not be called for an MFA-pending token")
		return db.User{}, nil
	}}
	mfa, _ := iss.IssueMFA(validUUID, "t1", 0)

	h := svc.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("handler must not be called for an MFA-pending token")
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Authorization", "Bearer "+mfa)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for MFA-pending token, got %d", rec.Code)
	}
}

// A user flagged must_change_password is locked out of normal routes (403) until they change it,
// but may still read their profile (GET /me) and submit the new password (PUT /me/password).
func TestMiddleware_MustChangePasswordGate(t *testing.T) {
	iss := NewTokenIssuer("secret", time.Hour)
	uid, _ := db.ParseUUID(validUUID)
	svc := &Service{issuer: iss, lookupUser: func(context.Context, pgtype.UUID) (db.User, error) {
		return db.User{ID: uid, Role: "user", MustChangePassword: true}, nil
	}}
	tok, _ := iss.Issue(validUUID, "t1", "user", 0, "")

	h := svc.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	call := func(method, path string) int {
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	if got := call(http.MethodGet, "/files"); got != http.StatusForbidden {
		t.Fatalf("gated route: got %d, want 403", got)
	}
	if got := call(http.MethodGet, "/me"); got != http.StatusOK {
		t.Fatalf("GET /me must be exempt: got %d, want 200", got)
	}
	if got := call(http.MethodPut, "/me/password"); got != http.StatusOK {
		t.Fatalf("PUT /me/password must be exempt: got %d, want 200", got)
	}
}

// Password change invalidates old sessions: token has ver=0 but DB has token_version=1 → 401.
func TestMiddleware_StaleTokenVersionRejected(t *testing.T) {
	iss := NewTokenIssuer("secret", time.Hour)
	uid, _ := db.ParseUUID(validUUID)
	svc := &Service{issuer: iss, lookupUser: func(context.Context, pgtype.UUID) (db.User, error) {
		return db.User{ID: uid, Role: "user", TokenVersion: 1}, nil // password was changed
	}}
	tok, _ := iss.Issue(validUUID, "t1", "user", 0, "") // old-version token

	h := svc.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("handler must not be called for a stale token version")
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for stale token, got %d", rec.Code)
	}
}

// Regression: authed responses must be no-store. Without this, the browser caches
// a response and later replays its stale X-Token; useApi saves the old token over
// the live one, expiring the session and forcing a spurious 401/logout on the next
// navigation (observed: clicking the Music settings tab / Books nav logged the user out).
func TestMiddleware_AuthedResponseIsNoStore(t *testing.T) {
	iss := NewTokenIssuer("secret", time.Hour)
	uid, _ := db.ParseUUID(validUUID)
	svc := &Service{issuer: iss, lookupUser: func(context.Context, pgtype.UUID) (db.User, error) {
		return db.User{ID: uid, Role: "user"}, nil
	}}
	tok, _ := iss.Issue(validUUID, "t1", "user", 0, "")
	h := svc.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/me/music", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store (authed responses must not be cached)", got)
	}
	if rec.Header().Get("X-Token") == "" {
		t.Errorf("X-Token missing — sliding renewal should still fire")
	}
}

// The renewed token must carry the lifetime the user picked, not the server default —
// otherwise the setting looks like it saved but the session still dies after an hour.
func TestMiddleware_RenewalUsesUserSessionTTL(t *testing.T) {
	iss := NewTokenIssuer("secret", time.Hour)
	uid, _ := db.ParseUUID(validUUID)

	// Device tokens are left out on purpose: verifying one hits the devices table, and
	// their lifetime is the server default anyway (see the middleware).
	renew := func(ttlMinutes int32) *Claims {
		t.Helper()
		svc := &Service{issuer: iss, lookupUser: func(context.Context, pgtype.UUID) (db.User, error) {
			return db.User{ID: uid, Role: "user", SessionTtlMinutes: ttlMinutes}, nil
		}}
		tok, _ := iss.Issue(validUUID, "t1", "user", 0, "")
		h := svc.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		fresh := rec.Header().Get("X-Token")
		if fresh == "" {
			t.Fatal("no X-Token on an authorized request")
		}
		claims, err := iss.Parse(fresh)
		if err != nil {
			t.Fatalf("parse renewed token: %v", err)
		}
		return claims
	}

	day := renew(1440)
	if got := day.ExpiresAt.Sub(day.IssuedAt.Time); got != 24*time.Hour {
		t.Fatalf("renewed lifetime = %v, want 24h", got)
	}
	if exp := renew(0).ExpiresAt; exp != nil {
		t.Fatalf("ExpiresAt=%v for a never-expiring session, want nil", exp)
	}
}
