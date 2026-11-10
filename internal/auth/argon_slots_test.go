package auth

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Each Argon2 run allocates its whole work area (64 MiB by default). Sign-in, the calendar
// feed password and the public album unlock ran as many at once as there were requests:
// twenty parallel logins meant over a gigabyte. However many checks are asked for, only a
// couple may hold a work area at the same time.
func TestPasswordChecksRunAFewAtATime(t *testing.T) {
	var active, peak atomic.Int32
	orig := argonHash
	argonHash = func(password, salt []byte, time_, memory uint32, threads uint8, keyLen uint32) []byte {
		n := active.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
		active.Add(-1)
		return make([]byte, keyLen)
	}
	defer func() { argonHash = orig }()

	hash, err := HashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = VerifyPassword("guess", hash)
		}()
	}
	wg.Wait()
	if p := peak.Load(); p > argonSlots {
		t.Fatalf("%d Argon2 runs at once, want at most %d", p, argonSlots)
	}
}
