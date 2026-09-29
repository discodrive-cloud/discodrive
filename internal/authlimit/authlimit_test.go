package authlimit

import (
	"fmt"
	"net/http/httptest"
	"testing"
	"time"
)

func TestLimiterCountsFailuresPerClientAndExpires(t *testing.T) {
	now := time.Unix(1000, 0)
	l := New(3, time.Minute, 16)
	l.now = func() time.Time { return now }

	a := httptest.NewRequest("GET", "/", nil)
	a.RemoteAddr = "198.51.100.1:1234"
	b := httptest.NewRequest("GET", "/", nil)
	b.RemoteAddr = "198.51.100.2:1234"

	for i := 0; i < 3; i++ {
		if l.Blocked(a) {
			t.Fatalf("blocked after %d failures", i)
		}
		l.Fail(a)
	}
	if !l.Blocked(a) {
		t.Fatal("not blocked after the limit")
	}
	if l.Blocked(b) {
		t.Fatal("another client is blocked too")
	}
	now = now.Add(time.Minute)
	if l.Blocked(a) {
		t.Fatal("still blocked after the window")
	}
}

func TestLimiterGroupsIPv6By64(t *testing.T) {
	l := New(2, time.Minute, 16)
	a := httptest.NewRequest("GET", "/", nil)
	a.RemoteAddr = "[2001:db8:1:2::1]:1"
	b := httptest.NewRequest("GET", "/", nil)
	b.RemoteAddr = "[2001:db8:1:2::ffff]:1"
	l.Fail(a)
	l.Fail(b)
	if !l.Blocked(a) {
		t.Fatal("addresses of one /64 are not counted together")
	}
}

func TestLimiterIsBounded(t *testing.T) {
	now := time.Unix(1000, 0)
	l := New(1, time.Minute, 4)
	l.now = func() time.Time { return now }
	for i := 0; i < 100; i++ {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = fmt.Sprintf("203.0.113.%d:1", i)
		l.Fail(r)
	}
	if len(l.peers) > 4 {
		t.Fatalf("tracks %d clients, want at most 4", len(l.peers))
	}
	now = now.Add(2 * time.Minute)
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "192.0.2.9:1"
	l.Fail(r)
	if !l.Blocked(r) {
		t.Fatal("expired entries were not reclaimed for a new client")
	}
}

func TestNilLimiter(t *testing.T) {
	var l *Limiter
	r := httptest.NewRequest("GET", "/", nil)
	l.Fail(r)
	if l.Blocked(r) {
		t.Fatal("nil limiter blocks")
	}
}
