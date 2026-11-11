package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"discodrive/internal/auth"
	"discodrive/internal/db"
)

func probeSession(svc *auth.Service, token string) int {
	r := httptest.NewRequest(http.MethodGet, "/files", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	svc.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })).ServeHTTP(rec, r)
	return rec.Code
}

// A token issued while the account's sessions never expired carries no expiry of its
// own. Once the account sets a lifetime, the session such a token belongs to must end
// after that long without use — it used to stay valid for good.
func TestSessionWithoutExpiryEndsAfterTheIdleTime(t *testing.T) {
	ctx := context.Background()
	pool, q, svc := bootstrapPairingDB(t)
	_, user, err := svc.Register(ctx, "idle@x.test", "password12")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := q.SetUserSessionTTL(ctx, db.SetUserSessionTTLParams{ID: user.ID, SessionTtlMinutes: 0}); err != nil {
		t.Fatalf("ttl 0: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM browser_sessions`); err != nil { // the one Register opened
		t.Fatalf("clear sessions: %v", err)
	}
	res, err := svc.Login(ctx, "idle@x.test", "password12")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	forgotten := res.Token // no exp: left on somebody else's computer

	idleFor := func(d time.Duration) {
		t.Helper()
		if _, err := pool.Exec(ctx, `UPDATE browser_sessions SET last_seen = now() - $1::interval WHERE user_id = $2`, d.String(), user.ID); err != nil {
			t.Fatalf("age the session: %v", err)
		}
	}
	lastSeenAge := func() time.Duration {
		t.Helper()
		var secs float64
		if err := pool.QueryRow(ctx, `SELECT EXTRACT(EPOCH FROM now() - max(last_seen))::float8 FROM browser_sessions WHERE user_id = $1`, user.ID).Scan(&secs); err != nil {
			t.Fatalf("last_seen: %v", err)
		}
		return time.Duration(secs * float64(time.Second))
	}

	// Sessions never expire: a year of silence changes nothing.
	idleFor(365 * 24 * time.Hour)
	if code := probeSession(svc, forgotten); code != http.StatusOK {
		t.Fatalf("never-expiring session after a year: %d", code)
	}
	// Using a session is what keeps it: the request above was noted.
	if age := lastSeenAge(); age > time.Minute {
		t.Fatalf("a used session was last seen %v ago", age)
	}

	if err := q.SetUserSessionTTL(ctx, db.SetUserSessionTTLParams{ID: user.ID, SessionTtlMinutes: 60}); err != nil {
		t.Fatalf("ttl 60: %v", err)
	}
	idleFor(30 * time.Minute)
	if code := probeSession(svc, forgotten); code != http.StatusOK {
		t.Fatalf("half the lifetime idle: %d", code)
	}
	idleFor(110 * time.Minute)
	if code := probeSession(svc, forgotten); code != http.StatusUnauthorized {
		t.Fatalf("idle longer than the lifetime: %d, want 401", code)
	}
	// A refused request does not bring the session back.
	if code := probeSession(svc, forgotten); code != http.StatusUnauthorized {
		t.Fatalf("second try: %d, want 401", code)
	}

	// The row itself goes once the links minted by the session (an hour) are over too.
	if n, err := q.DeleteIdleBrowserSessions(ctx); err != nil || n != 0 {
		t.Fatalf("collected %d sessions 110 minutes idle of 60+60, err %v", n, err)
	}
	idleFor(2*time.Hour + time.Minute)
	if n, err := q.DeleteIdleBrowserSessions(ctx); err != nil || n != 1 {
		t.Fatalf("collected %d sessions, want 1 (err %v)", n, err)
	}
}
