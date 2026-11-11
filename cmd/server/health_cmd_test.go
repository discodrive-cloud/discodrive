package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The container healthcheck: is the server answering on its port? nginx waits for it, so
// a start that is still applying migrations does not answer requests with 502.
func TestHealthcheck(t *testing.T) {
	answer := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(answer) }))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())

	if code := runHealthcheck("127.0.0.1", port, time.Second); code != 0 {
		t.Fatalf("a serving server: exit %d", code)
	}
	// Behind nginx the server refuses plain HTTP (403 "Use HTTPS"): it is up all the same.
	answer = http.StatusForbidden
	if code := runHealthcheck("127.0.0.1", port, time.Second); code != 0 {
		t.Fatalf("a server that refuses plain HTTP: exit %d", code)
	}
	answer = http.StatusInternalServerError
	if code := runHealthcheck("127.0.0.1", port, time.Second); code == 0 {
		t.Fatal("a failing server passed")
	}
	// APP_HOST=0.0.0.0 is where it listens, not where to call it.
	answer = http.StatusOK
	if code := runHealthcheck("0.0.0.0", port, time.Second); code != 0 {
		t.Fatalf("listening on every address: exit %d", code)
	}
	srv.Close()
	if code := runHealthcheck("127.0.0.1", port, time.Second); code == 0 {
		t.Fatal("nobody listening, and the check passed")
	}
}
