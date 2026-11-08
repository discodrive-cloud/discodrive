package storage_test

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"discodrive/internal/db"
	"discodrive/internal/quota"
	"discodrive/internal/storage"
)

// gatedCopy holds the first two Copy calls until both have arrived (or a timeout),
// so two restores overlap between their quota decision and their commit.
type gatedCopy struct {
	storage.Storage
	n    atomic.Int32
	both chan struct{}
	once sync.Once
}

func (g *gatedCopy) Copy(src, dst string) error {
	if g.n.Add(1) <= 2 {
		if g.n.Load() >= 2 {
			g.once.Do(func() { close(g.both) })
		}
		select {
		case <-g.both:
		case <-time.After(2 * time.Second):
		}
	}
	return g.Storage.Copy(src, dst)
}

// Restoring an old version writes its bytes again. Two restores of different files
// must not both pass a quota check made before either has written anything.
func TestRestoreConcurrentRespectsQuota(t *testing.T) {
	gate := &gatedCopy{both: make(chan struct{})}
	fs, q, userID, _ := setupFSWith(t, func(s storage.Storage) storage.Storage { gate.Storage = s; return gate })
	fs.SetQuota(quota.New(q, 0))
	ctx := context.Background()

	ids := map[string]string{}
	for _, name := range []string{"a.bin", "b.bin"} {
		res, err := fs.Push(ctx, userID, nil, name, nil, "", bytes.NewReader(bytes.Repeat([]byte("a"), 200)))
		if err != nil {
			t.Fatal(err)
		}
		v := res.Node.Version
		if _, err := fs.Push(ctx, userID, nil, name, &v, "", bytes.NewReader(bytes.Repeat([]byte("b"), 200))); err != nil {
			t.Fatal(err)
		}
		ids[name] = db.UUIDString(res.Node.ID)
	}
	setQuota(t, q, userID, 1000) // 800 used: room for one 200-byte restore, not two

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, name := range []string{"a.bin", "b.bin"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = fs.Restore(ctx, userID, ids[name], 1)
		}()
	}
	wg.Wait()

	uid, _ := db.ParseUUID(userID)
	used, err := q.UserStorageUsage(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if used > 1000 {
		t.Fatalf("used = %d over a 1000 quota (errors: %v, %v)", used, errs[0], errs[1])
	}
	refused := 0
	for _, e := range errs {
		if errors.Is(e, quota.ErrExceeded) {
			refused++
		} else if e != nil {
			t.Fatalf("unexpected error: %v", e)
		}
	}
	if refused != 1 {
		t.Fatalf("%d restores refused, want exactly 1", refused)
	}
}
