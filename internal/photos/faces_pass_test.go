package photos

import "testing"

// While the queue is busy a pass starts after a step of new faces; the step grows with
// the library, so a first run over a big one is a few dozen passes, not a hundred.
func TestPassDueGrowsWithTheLibrary(t *testing.T) {
	for _, c := range []struct {
		fresh, known int
		want         bool
	}{
		{1999, 0, false}, {2000, 0, true}, // a new library: the fixed step
		{2000, 16000, true}, {2000, 16008, false}, // an eighth takes over
		{24999, 200000, false}, {25000, 200000, true},
	} {
		if got := passDue(c.fresh, c.known); got != c.want {
			t.Errorf("passDue(%d new, %d known) = %v, want %v", c.fresh, c.known, got, c.want)
		}
	}
	passes, known := 0, 0
	for known < 200000 {
		step := 0
		for !passDue(step, known) {
			step += 100
		}
		known += step
		passes++
	}
	if passes > 40 {
		t.Errorf("a first run over 200 000 faces makes %d passes", passes)
	}
}
