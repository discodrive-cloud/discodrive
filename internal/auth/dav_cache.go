package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"sync"
	"time"
)

// davCacheTTL is how long a successful Argon2 check of a WebDAV app password is reused.
// It is counted from the check and not extended by use, so every client is re-verified
// with Argon2 at least this often.
const davCacheTTL = 5 * time.Minute

// davCacheMax bounds the entries. Only successful checks are stored, so the cache
// cannot be filled without valid credentials.
const davCacheMax = 4096

// davCache remembers recent successful WebDAV password checks, so a client's request
// burst (Finder, rclone, CalDAV sync) pays for Argon2 — 64 MiB and ~0.5 s on small
// hardware — once per TTL instead of once per request.
//
// It holds no passwords: the key is an HMAC of email and password under a per-process
// random key. It is not the source of validity either: every hit is confirmed against
// the current database row (device present, same secret hash, token version and
// password-change state still valid), so revocation takes effect on the next request.
type davCache struct {
	mu      sync.Mutex
	key     []byte
	entries map[[sha256.Size]byte]davCacheEntry
	now     func() time.Time
}

type davCacheEntry struct {
	userID, deviceID, secretHash string
	expires                      time.Time
}

func newDAVCache() *davCache {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	return &davCache{key: key, entries: make(map[[sha256.Size]byte]davCacheEntry), now: time.Now}
}

func (c *davCache) id(email, password string) [sha256.Size]byte {
	m := hmac.New(sha256.New, c.key)
	m.Write([]byte(email))
	m.Write([]byte{0})
	m.Write([]byte(password))
	var out [sha256.Size]byte
	copy(out[:], m.Sum(nil))
	return out
}

// lookup returns a live entry for the credentials, if one was stored.
func (c *davCache) lookup(email, password string) (davCacheEntry, bool) {
	id := c.id(email, password)
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[id]
	if !ok || !c.now().Before(e.expires) {
		return davCacheEntry{}, false
	}
	return e, true
}

// store records a successful Argon2 check; e.expires is set here.
func (c *davCache) store(email, password string, e davCacheEntry) {
	id := c.id(email, password)
	now := c.now()
	e.expires = now.Add(davCacheTTL)
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= davCacheMax {
		for k, v := range c.entries {
			if !now.Before(v.expires) {
				delete(c.entries, k)
			}
		}
		for k := range c.entries { // still full of live entries: drop an arbitrary one
			if len(c.entries) < davCacheMax {
				break
			}
			delete(c.entries, k)
		}
	}
	c.entries[id] = e
}

func (c *davCache) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}
