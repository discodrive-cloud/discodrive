package appleprofile

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"
)

// Tickets bridges an authenticated app request to Safari's profile download.
// Entries expire in memory, are bounded, and contain no account passwords.
// Multiple reads are allowed because profile installation can retry a download.
type Tickets struct {
	mu      sync.Mutex
	entries map[string]ticket
}
type ticket struct {
	owner string
	data  []byte
	until time.Time
}

const Lifetime = 10 * time.Minute

func (t *Tickets) Put(owner string, data []byte) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.entries == nil {
		t.entries = make(map[string]ticket)
	}
	now := time.Now()
	for k, v := range t.entries {
		if !v.until.After(now) {
			delete(t.entries, k)
		}
	}
	n := 0
	for _, v := range t.entries {
		if v.owner == owner {
			n++
		}
	}
	if len(t.entries) >= 1024 || n >= 5 {
		return "", errors.New("too many pending profiles")
	}
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		return "", err
	}
	token := hex.EncodeToString(key[:])
	t.entries[token] = ticket{owner: owner, data: append([]byte(nil), data...), until: now.Add(Lifetime)}
	return token, nil
}
func (t *Tickets) Get(key string) ([]byte, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	entry, ok := t.entries[key]
	if !ok {
		return nil, false
	}
	if !entry.until.After(time.Now()) {
		delete(t.entries, key)
		return nil, false
	}
	return append([]byte(nil), entry.data...), true
}
