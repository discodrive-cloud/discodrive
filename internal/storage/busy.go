package storage

import (
	"strings"
	"sync"
)

// busyPaths tracks tree paths whose disk change is done but whose database change is
// not committed yet (an upload moved into place, a rename or move in progress). Rescan
// skips them: it would otherwise import the file itself, or take a renamed node for
// missing, racing the operation that owns the path.
type busyPaths struct {
	mu    sync.Mutex
	paths map[string]int
}

// hold marks rels (and everything under them) busy until the returned release runs.
func (b *busyPaths) hold(rels ...string) (release func()) {
	b.mu.Lock()
	if b.paths == nil {
		b.paths = make(map[string]int)
	}
	for _, r := range rels {
		b.paths[r]++
	}
	b.mu.Unlock()
	return func() {
		b.mu.Lock()
		for _, r := range rels {
			if b.paths[r]--; b.paths[r] <= 0 {
				delete(b.paths, r)
			}
		}
		b.mu.Unlock()
	}
}

// covers reports whether rel is a busy path or lies under one.
func (b *busyPaths) covers(rel string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for p := range b.paths {
		if rel == p || strings.HasPrefix(rel, p+"/") {
			return true
		}
	}
	return false
}
