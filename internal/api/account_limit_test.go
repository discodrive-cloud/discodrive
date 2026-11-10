package api

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"discodrive/internal/auth"
	"discodrive/internal/db"
)

// A stolen bearer token must not allow guessing the account password without bound just
// because every guess comes from a new address: wrong guesses are counted per account.
func TestPasswordGuessesAreCountedPerAccount(t *testing.T) {
	ctx := context.Background()
	_, q, svc := bootstrapPairingDB(t)
	victim, _, err := svc.Register(ctx, "victim@x.test", "password12")
	if err != nil {
		t.Fatal(err)
	}
	bystander, _, err := svc.Register(ctx, "bystander@x.test", "password12")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{auth: svc, q: q, loginLimiter: newLoginLimiter(), accountLimiter: newAccountLimiter()}
	h := svc.Middleware(s.passwordChecked(s.handleChangePassword))

	guess := func(tok, current string, n int) int {
		body := fmt.Sprintf(`{"current_password":%q,"new_password":"password34"}`, current)
		req := httptest.NewRequest(http.MethodPut, "/me/password", bytes.NewReader([]byte(body)))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.RemoteAddr = fmt.Sprintf("203.0.%d.%d:4000", n/250, n%250+1) // a new address every time
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	blockedAt := -1
	for i := 0; i < 40; i++ {
		code := guess(victim, "wrong-guess", i)
		if code == http.StatusTooManyRequests {
			blockedAt = i
			break
		}
		if code != http.StatusUnauthorized {
			t.Fatalf("guess %d: got %d, want 401 or 429", i, code)
		}
	}
	if blockedAt < 0 {
		t.Fatal("40 wrong passwords from 40 addresses were all checked: nothing counts guesses per account")
	}
	if code := guess(bystander, "password12", 100); code != http.StatusOK {
		t.Fatalf("another account changing its password: got %d, want 200", code)
	}
}

// One IPv6 host usually owns a whole /64: changing the address inside it must not buy a
// fresh budget of attempts.
func TestRateLimitGroupsIPv6ByPrefix(t *testing.T) {
	s := &Server{loginLimiter: newLoginLimiter()}
	h := s.rateLimited(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	blocked := false
	for i := 0; i < 40; i++ {
		req := httptest.NewRequest(http.MethodPost, "/auth/login", nil)
		req.RemoteAddr = fmt.Sprintf("[2001:db8:1:2::%x]:4000", i+1)
		rec := httptest.NewRecorder()
		h(rec, req)
		if rec.Code == http.StatusTooManyRequests {
			blocked = true
			break
		}
	}
	if !blocked {
		t.Fatal("40 attempts from 40 addresses of one /64 were all let through")
	}
	req := httptest.NewRequest(http.MethodPost, "/auth/login", nil)
	req.RemoteAddr = "[2001:db8:1:3::1]:4000"
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("a neighbouring /64 got %d, want 200", rec.Code)
	}
}

// Wrong second-factor codes are counted per account: with the password known, the six
// digits must not be guessable by spreading attempts over many addresses and fresh
// sign-in tokens.
func TestSecondFactorGuessesAreCountedPerAccount(t *testing.T) {
	ctx := context.Background()
	_, q, svc := bootstrapPairingDB(t)
	_, u, err := svc.Register(ctx, "mfa@x.test", "password12")
	if err != nil {
		t.Fatal(err)
	}
	issuer := auth.NewTokenIssuer("secret", time.Hour)
	s := &Server{auth: svc, q: q, loginLimiter: newLoginLimiter(), accountLimiter: newAccountLimiter()}
	h := s.rateLimited(s.handleMFATOTP)

	blocked := false
	for i := 0; i < 40; i++ {
		tok, err := issuer.IssueMFA(db.UUIDString(u.ID), db.UUIDString(u.TenantID), u.TokenVersion)
		if err != nil {
			t.Fatal(err)
		}
		body := fmt.Sprintf(`{"mfa_token":%q,"code":"%06d"}`, tok, i)
		req := httptest.NewRequest(http.MethodPost, "/auth/mfa/totp", bytes.NewReader([]byte(body)))
		req.RemoteAddr = fmt.Sprintf("203.0.113.%d:4000", i+1)
		rec := httptest.NewRecorder()
		h(rec, req)
		if rec.Code == http.StatusTooManyRequests {
			blocked = true
			break
		}
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("guess %d: got %d (%s), want 401 or 429", i, rec.Code, rec.Body)
		}
	}
	if !blocked {
		t.Fatal("40 wrong codes from 40 addresses were all checked")
	}
}
