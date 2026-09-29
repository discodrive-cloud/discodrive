// Package authlimit throttles failed credential checks per client address for
// the media protocols (Subsonic, OPDS, kosync). Only failures count: clients with
// a valid credential poll a lot and are never limited.
package authlimit

import (
	"net/http"
	"net/netip"
	"sync"
	"time"

	"discodrive/internal/httpsecurity"
)

// Defaults: ten failed checks per client per minute, at most 4096 clients tracked.
const (
	DefaultLimit    = 10
	DefaultWindow   = time.Minute
	DefaultMaxPeers = 4096
)

type entry struct {
	failures int
	until    time.Time
}

// Limiter counts failed attempts per client in a fixed window. In memory, bounded
// to maxPeers clients, no background goroutine: expired entries are dropped when
// the table fills up. A nil *Limiter never blocks and records nothing.
type Limiter struct {
	mu       sync.Mutex
	limit    int
	window   time.Duration
	maxPeers int
	peers    map[string]*entry
	now      func() time.Time
}

// New returns a Limiter allowing limit failures per window for each client.
func New(limit int, window time.Duration, maxPeers int) *Limiter {
	return &Limiter{limit: limit, window: window, maxPeers: maxPeers, peers: map[string]*entry{}, now: time.Now}
}

// NewDefault returns a Limiter with the package defaults.
func NewDefault() *Limiter { return New(DefaultLimit, DefaultWindow, DefaultMaxPeers) }

// Blocked reports whether r's client has used up its failure budget.
func (l *Limiter) Blocked(r *http.Request) bool {
	if l == nil {
		return false
	}
	k := key(r)
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.peers[k]
	return e != nil && e.failures >= l.limit && l.now().Before(e.until)
}

// Fail records one failed attempt by r's client.
func (l *Limiter) Fail(r *http.Request) {
	if l == nil {
		return
	}
	k := key(r)
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	e := l.peers[k]
	if e != nil && !now.Before(e.until) {
		e.failures, e.until = 0, now.Add(l.window)
	}
	if e == nil {
		if len(l.peers) >= l.maxPeers {
			for pk, pe := range l.peers {
				if !now.Before(pe.until) {
					delete(l.peers, pk)
				}
			}
		}
		if len(l.peers) >= l.maxPeers {
			// Table full of live entries: do not grow. Credentials on these
			// endpoints are random per-service passwords/keys, so this is defence
			// in depth, and memory stays bounded.
			return
		}
		e = &entry{until: now.Add(l.window)}
		l.peers[k] = e
	}
	e.failures++
}

// key groups IPv6 clients by /64 (one host usually owns the whole prefix) and
// IPv4 clients by address.
func key(r *http.Request) string {
	raw := httpsecurity.ClientIP(r)
	ip, err := netip.ParseAddr(raw)
	if err != nil {
		if ap, err2 := netip.ParseAddrPort(raw); err2 == nil {
			ip = ap.Addr()
		} else {
			return raw
		}
	}
	ip = ip.Unmap()
	if ip.Is6() {
		if p, err := ip.Prefix(64); err == nil {
			return p.String()
		}
	}
	return ip.String()
}
