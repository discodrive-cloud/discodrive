package auth

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"discodrive/internal/httpsecurity"
)

var ErrDAVBusy = errors.New("DAV authentication temporarily limited")

// Shared across services/protocols in this process. Two simultaneous DAV checks
// cost at most two normal 64 MiB Argon2 workspaces. Brief legitimate bursts
// may wait up to one second in a bounded queue, never allocate Argon2 memory there.
var davCheckSlots = make(chan struct{}, 2)
var davWaitSlots = make(chan struct{}, 32)

const davFailureLimit = 10
const davPeerLimit = 4096

type davAttempt struct {
	failures, active int
	until            time.Time
}
type davGuard struct {
	mu    sync.Mutex
	peers map[string]*davAttempt
}

func acquireDAVSlot(ctx context.Context) error {
	select {
	case davCheckSlots <- struct{}{}:
		return nil
	default:
	}
	select {
	case davWaitSlots <- struct{}{}:
	default:
		return ErrDAVBusy
	}
	defer func() { <-davWaitSlots }()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case davCheckSlots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return ErrDAVBusy
	}
}

func (g *davGuard) begin(ctx context.Context, peer string) (func(bool), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	g.mu.Lock()
	now := time.Now()
	if g.peers == nil {
		g.peers = make(map[string]*davAttempt)
	}
	for key, v := range g.peers {
		if !now.Before(v.until) && v.active == 0 {
			delete(g.peers, key)
		}
	}
	v := g.peers[peer]
	if v == nil {
		if len(g.peers) >= davPeerLimit {
			g.mu.Unlock()
			return nil, ErrDAVBusy
		}
		v = &davAttempt{until: now.Add(time.Minute)}
		g.peers[peer] = v
	}
	if v.failures+v.active >= davFailureLimit {
		g.mu.Unlock()
		return nil, ErrDAVBusy
	}
	// Reserve an attempt before waiting, so parallel requests cannot all inspect
	// the same remaining failure budget and start expensive work independently.
	v.active++
	g.mu.Unlock()
	if err := acquireDAVSlot(ctx); err != nil {
		g.mu.Lock()
		v.active--
		g.mu.Unlock()
		return nil, err
	}
	return func(success bool) {
		g.mu.Lock()
		v.active--
		if !success {
			v.failures++
		}
		g.mu.Unlock()
		<-davCheckSlots
	}, nil
}

// AuthenticateDAV is shared by WebDAV, CalDAV and CardDAV. Only failed checks
// spend the per-IP budget; valid bulk synchronization has no per-minute quota.
func (s *Service) AuthenticateDAV(r *http.Request) (string, string, error) {
	email, password, ok := r.BasicAuth()
	if !ok {
		return "", "", ErrInvalidCreds
	}
	return s.checkDAVPassword(r.Context(), email, password, httpsecurity.ClientIP(r))
}

func (s *Service) checkDAVPassword(ctx context.Context, email, password, peer string) (string, string, error) {
	if len(email) > 320 || len(password) > 1024 {
		return "", "", ErrInvalidCreds
	}
	done, err := s.davGuard.begin(ctx, peer)
	if err != nil {
		return "", "", err
	}
	success := false
	defer func() { done(success) }()
	uid, did, ok := s.verifyWebdavPassword(ctx, email, password)
	if !ok {
		return "", "", ErrInvalidCreds
	}
	success = true
	return uid, did, nil
}

// VerifyWebdavPassword keeps the service API used by non-HTTP callers guarded.
func (s *Service) VerifyWebdavPassword(ctx context.Context, email, password string) (string, string, bool) {
	uid, did, err := s.checkDAVPassword(ctx, email, password, "internal")
	return uid, did, err == nil
}
