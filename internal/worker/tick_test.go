package worker

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// A panicking job used to crash the whole server from its ticker goroutine. It must
// be logged and the job must run again on the next tick.
func TestTickSurvivesPanickingJob(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		(&Worker{}).tick(ctx, 5*time.Millisecond, "boom", func(context.Context) error {
			if calls.Add(1) == 1 {
				panic("bad data")
			}
			return nil
		})
	}()
	deadline := time.Now().Add(5 * time.Second)
	for calls.Load() < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("job ran %d times; the ticker died after the panic", calls.Load())
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
}
