package auth

import (
	"runtime"
	"runtime/debug"
	"sync/atomic"
	"testing"
	"time"
)

var sink []byte

// A password check allocates Argon2's 64 MiB work area in one piece. After one check the
// GC target sits near twice that, so ~100 MiB of garbage can pile up unswept — and the
// next check's 64 MiB landed on top of it: on a 128 MB box that was an OOM kill. The
// check must start from a collected heap.
func TestPasswordCheckDoesNotStackOnGarbage(t *testing.T) {
	hash, err := HashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	defer debug.SetGCPercent(debug.SetGCPercent(100))

	// Raise the GC target the way a previous check does: a large live heap, collected.
	sink = make([]byte, 64<<20)
	runtime.GC()
	sink = nil
	// Garbage below that target: no collection will run on its own.
	for range 60 {
		sink = make([]byte, 1<<20)
	}
	sink = nil

	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	var peak atomic.Uint64
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		var m runtime.MemStats
		for {
			runtime.ReadMemStats(&m)
			if m.HeapAlloc > peak.Load() {
				peak.Store(m.HeapAlloc)
			}
			select {
			case <-stop:
				return
			case <-time.After(time.Millisecond):
			}
		}
	}()
	ok, err := VerifyPassword("correct horse", hash)
	close(stop)
	<-done
	if err != nil || !ok {
		t.Fatalf("verify: %v %v", ok, err)
	}
	// Argon2's 64 MiB must replace the garbage, not stack on it: the heap may not grow
	// by anything like the work area's size over where it already was.
	if p := peak.Load(); p > before.HeapAlloc+32<<20 {
		t.Fatalf("heap went from %d to %d MiB during a password check: Argon2 stacked on uncollected garbage",
			before.HeapAlloc>>20, p>>20)
	}
}
