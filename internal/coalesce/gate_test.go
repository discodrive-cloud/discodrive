package coalesce

import (
	"sync/atomic"
	"testing"
)

// A second request while a run is in flight must not start a parallel run; the
// running one goes once more so nothing that arrived meanwhile is missed.
func TestGateCoalescesConcurrentRuns(t *testing.T) {
	var g Gate
	var runs atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan bool)
	go func() {
		done <- g.Run("u1", func() {
			if runs.Add(1) == 1 {
				close(started)
				<-release
			}
		})
	}()
	<-started

	for range 3 {
		if g.Run("u1", func() { t.Error("a second run started in parallel") }) {
			t.Fatal("Run reported it ran while the key was busy")
		}
	}
	if !g.Run("u2", func() {}) {
		t.Fatal("another key must run independently")
	}
	close(release)
	if !<-done {
		t.Fatal("the first Run must report it ran")
	}
	if got := runs.Load(); got != 2 {
		t.Fatalf("fn ran %d times, want 2 (initial + one catch-up for the coalesced requests)", got)
	}
	if !g.Run("u1", func() {}) {
		t.Fatal("key must be free again after the run finished")
	}
}
