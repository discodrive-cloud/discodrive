package api

import (
	"net/http"
	"sync"
	"time"
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
