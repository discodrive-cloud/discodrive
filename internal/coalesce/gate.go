// Package coalesce keeps at most one run of a job per key in flight.
package coalesce

import "sync"

// Gate runs at most one fn per key at a time. A Run that arrives while its key is
// busy does not start a parallel run: it asks the running one to go once more when it
// finishes (so work that arrived meanwhile is not missed) and returns false at once.
// The zero value is ready to use.
type Gate struct {
	mu    sync.Mutex
	again map[string]bool // key present = running; value = another pass requested
}

// Run executes fn for key unless a run for key is in flight, and reports whether it did.
func (g *Gate) Run(key string, fn func()) bool {
	g.mu.Lock()
	if g.again == nil {
		g.again = make(map[string]bool)
	}
	if _, busy := g.again[key]; busy {
		g.again[key] = true
		g.mu.Unlock()
		return false
	}
	g.again[key] = false
	g.mu.Unlock()

	for {
		func() {
			// A panicking fn must not leave the key marked busy forever.
			defer func() {
				if r := recover(); r != nil {
					g.mu.Lock()
					delete(g.again, key)
					g.mu.Unlock()
					panic(r)
				}
			}()
			fn()
		}()
		g.mu.Lock()
		if g.again[key] {
			g.again[key] = false
			g.mu.Unlock()
			continue
		}
		delete(g.again, key)
		g.mu.Unlock()
		return true
	}
}
