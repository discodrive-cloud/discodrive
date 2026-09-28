package webdav

import (
	"context"
	"sync"

	"discodrive/internal/db"
)

// nodeMemo remembers nodes already fetched during one request, keyed by cleaned DAV
// path. A PROPFIND walk looks every child up about three times right after listing
// the folder that returned them; the memo answers those from the listing. Any change
// made through the file system in the same request clears it.
type nodeMemo struct {
	mu    sync.Mutex
	nodes map[string]db.Node
}

type nodeMemoKey struct{}

// withNodeMemo gives the request its own memo; without one every lookup queries the
// database.
func withNodeMemo(ctx context.Context) context.Context {
	return context.WithValue(ctx, nodeMemoKey{}, &nodeMemo{nodes: make(map[string]db.Node)})
}

func memoFrom(ctx context.Context) *nodeMemo {
	m, _ := ctx.Value(nodeMemoKey{}).(*nodeMemo)
	return m
}

func (m *nodeMemo) get(name string) (db.Node, bool) {
	if m == nil {
		return db.Node{}, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	n, ok := m.nodes[name]
	return n, ok
}

func (m *nodeMemo) put(name string, n db.Node) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.nodes[name] = n
	m.mu.Unlock()
}

func (m *nodeMemo) clear() {
	if m == nil {
		return
	}
	m.mu.Lock()
	clear(m.nodes)
	m.mu.Unlock()
}
