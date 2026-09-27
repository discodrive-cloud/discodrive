package auth

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestDAVGuardSuccessFailureAndPeerIsolation(t *testing.T) {
	var g davGuard
	for i := 0; i < 100; i++ {
		done, err := g.begin(context.Background(), "client-a")
		if err != nil {
			t.Fatal(err)
		}
		done(true)
	}
	for i := 0; i < davFailureLimit; i++ {
		done, err := g.begin(context.Background(), "client-a")
		if err != nil {
			t.Fatal(err)
		}
		done(false)
	}
	if _, err := g.begin(context.Background(), "client-a"); !errors.Is(err, ErrDAVBusy) {
		t.Fatalf("exhausted budget=%v", err)
	}
	done, err := g.begin(context.Background(), "client-b")
	if err != nil {
		t.Fatal(err)
	}
	done(true)
	g.peers["client-a"].until = time.Now().Add(-time.Second)
	done, err = g.begin(context.Background(), "client-a")
	if err != nil {
		t.Fatal(err)
	}
	done(true)
}

func TestDAVGuardGlobalConcurrencyAndCancellation(t *testing.T) {
	var first, second davGuard
	release := make(chan struct{})
	started := make(chan error, 32)
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			g := &first
			if i%2 == 0 {
				g = &second
			}
			done, err := g.begin(context.Background(), fmt.Sprint(i))
			started <- err
			if err == nil {
				<-release
				done(true)
			}
		}(i)
	}
	active := 0
	for i := 0; i < 32; i++ {
		if err := <-started; err == nil {
			active++
		} else if !errors.Is(err, ErrDAVBusy) {
			t.Fatal(err)
		}
	}
	close(release)
	wg.Wait()
	if active != 2 {
		t.Fatalf("concurrent checks=%d want2", active)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := first.begin(ctx, "cancelled"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	done, err := first.begin(context.Background(), "after")
	if err != nil {
		t.Fatal(err)
	}
	done(true)
}

func TestDAVGuardReservesAttemptsAndBoundsMemory(t *testing.T) {
	var g davGuard
	g.peers = map[string]*davAttempt{"a": {failures: 9, until: time.Now().Add(time.Minute)}}
	done, err := g.begin(context.Background(), "a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.begin(context.Background(), "a"); !errors.Is(err, ErrDAVBusy) {
		done(true)
		t.Fatal("parallel attempt exceeded budget")
	}
	done(false)
	for len(g.peers) < davPeerLimit {
		g.peers[fmt.Sprint(len(g.peers))] = &davAttempt{until: time.Now().Add(time.Minute)}
	}
	if _, err := g.begin(context.Background(), "new"); !errors.Is(err, ErrDAVBusy) {
		t.Fatal("unbounded peer map")
	}
}

func TestDAVGuardQueuedBurstAndCancellation(t *testing.T) {
	var g davGuard
	a, err := g.begin(context.Background(), "a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := g.begin(context.Background(), "b")
	if err != nil {
		a(true)
		t.Fatal(err)
	}
	type result struct {
		done func(bool)
		err  error
	}
	ctx, cancel := context.WithCancel(context.Background())
	pending := make(chan result, 1)
	go func() { done, err := g.begin(ctx, "queued"); pending <- result{done, err} }()
	deadline := time.Now().Add(500 * time.Millisecond)
	for len(davWaitSlots) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	res := <-pending
	if res.done != nil {
		res.done(true)
	}
	if !errors.Is(res.err, context.Canceled) {
		a(true)
		b(true)
		t.Fatalf("queued cancel=%v", res.err)
	}
	go func() { done, err := g.begin(context.Background(), "queued-valid"); pending <- result{done, err} }()
	deadline = time.Now().Add(500 * time.Millisecond)
	for len(davWaitSlots) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	a(true)
	res = <-pending
	b(true)
	if res.err != nil {
		t.Fatal(res.err)
	}
	res.done(true)
	if len(davCheckSlots) != 0 || len(davWaitSlots) != 0 {
		t.Fatal("leaked admission slot")
	}
}
