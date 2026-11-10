package api

import (
	"net/http"
	"sync"
	"time"

	"discodrive/internal/auth"
)

// rateLimiter is a fixed-window per-key attempt counter (login brute-force protection).
// In-memory for our single binary; the map is reset at the start of each window
// so it does not grow without bound.
type rateLimiter struct {
	mu          sync.Mutex
	counts      map[string]int
	limit       int
	window      time.Duration
	windowStart time.Time
}

// newLoginLimiter allows at most limit attempts per IP per minute.
func newLoginLimiter() *rateLimiter {
	return &rateLimiter{counts: make(map[string]int), limit: 10, window: time.Minute, windowStart: time.Now()}
}

// allow records an attempt and reports whether the key is within the limit.
func (rl *rateLimiter) allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.roll()
	if rl.counts[key] >= rl.limit {
		return false
	}
	rl.counts[key]++
	return true
}

// peek reports whether the key is within the limit without consuming an attempt.
func (rl *rateLimiter) peek(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.roll()
	return rl.counts[key] < rl.limit
}

// record consumes one attempt for the key.
func (rl *rateLimiter) record(key string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.roll()
	rl.counts[key]++
}

// roll resets the window when it has elapsed. Callers must hold mu.
func (rl *rateLimiter) roll() {
	if time.Since(rl.windowStart) > rl.window {
		rl.counts = make(map[string]int)
		rl.windowStart = time.Now()
	}
}

// rateLimited wraps a handler, rejecting requests that exceed the per-IP limit.
func (s *Server) rateLimited(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.loginLimiter.allow(clientIP(r)) {
			writeError(w, http.StatusTooManyRequests, "too many attempts, please try again later")
			return
		}
		h(w, r)
	}
}

// statusRecorder captures the response status code written by the wrapped handler.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (sr *statusRecorder) WriteHeader(code int) {
	sr.status = code
	sr.ResponseWriter.WriteHeader(code)
}

// authLimited is like rateLimited, but only FAILED attempts (4xx/5xx) consume the
// per-IP budget. A client presenting a valid credential is never throttled — bulk
// operations from dd-cli/dd-mcp (one token exchange per process) must not hit 429 —
// while brute-forcing invalid tokens is still limited.
func (s *Server) authLimited(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := clientIP(r)
		if !s.loginLimiter.peek(ip) {
			writeError(w, http.StatusTooManyRequests, "too many attempts, please try again later")
			return
		}
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		h(rec, r)
		if rec.status >= 400 {
			s.loginLimiter.record(ip)
		}
	}
}

// newPollLimiter is the ceiling for /pair/token polling (the daemon polls frequently).
func newPollLimiter() *rateLimiter {
	return &rateLimiter{counts: make(map[string]int), limit: 120, window: time.Minute, windowStart: time.Now()}
}

// pollLimited is like rateLimited but uses the more generous poll-specific limiter.
func (s *Server) pollLimited(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.pollLimiter.allow(clientIP(r)) {
			writeError(w, http.StatusTooManyRequests, "too many requests, please try again later")
			return
		}
		h(w, r)
	}
}

// reserve counts in-flight checks too; successful checks return only their own window's slot.
func (rl *rateLimiter) reserve(key string) (func(), bool) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.roll()
	if rl.counts[key] >= rl.limit {
		return nil, false
	}
	rl.counts[key]++
	window := rl.windowStart
	return func() {
		rl.mu.Lock()
		defer rl.mu.Unlock()
		if rl.windowStart == window && rl.counts[key] > 0 {
			rl.counts[key]--
		}
	}, true
}

// sharesPerMinute caps share requests (files, calendars, address books, task lists) per
// user. Each new share mails the recipient, and removing a share and adding it again makes
// it new: without a cap that loop would let one user flood another's inbox.
const sharesPerMinute = 30

func newShareLimiter() *rateLimiter {
	return &rateLimiter{counts: make(map[string]int), limit: sharesPerMinute, window: time.Minute, windowStart: time.Now()}
}

// shareAllowed counts a share request of the signed-in user; false means it was answered
// with 429.
func (s *Server) shareAllowed(w http.ResponseWriter, r *http.Request) bool {
	if s.shareLimiter != nil && !s.shareLimiter.allow(auth.UserID(r.Context())) {
		writeError(w, http.StatusTooManyRequests, "too many share requests, please try again later")
		return false
	}
	return true
}

// accountGuesses is how many wrong passwords one account may present per window on the
// signed-in endpoints that re-check it. The per-IP limiter alone does not bound a holder
// of a stolen token who changes address between guesses.
const (
	accountGuesses     = 10
	accountGuessWindow = 15 * time.Minute
)

func newAccountLimiter() *rateLimiter {
	return &rateLimiter{counts: make(map[string]int), limit: accountGuesses, window: accountGuessWindow, windowStart: time.Now()}
}

// passwordChecked wraps a signed-in handler that verifies the account password (or
// another secret of the account): per-IP limit as in rateLimited, plus a per-account
// budget that only refused attempts (401/403) consume.
func (s *Server) passwordChecked(h http.HandlerFunc) http.HandlerFunc {
	return s.rateLimited(func(w http.ResponseWriter, r *http.Request) {
		refund, ok := s.accountLimiter.reserve(auth.UserID(r.Context()))
		if !ok {
			writeError(w, http.StatusTooManyRequests, "too many attempts, please try again later")
			return
		}
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		h(rec, r)
		if rec.status != http.StatusUnauthorized && rec.status != http.StatusForbidden {
			refund()
		}
	})
}
