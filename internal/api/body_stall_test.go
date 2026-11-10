package api

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// An upload holds one of the few upload slots (and its database connection) for as long
// as its body is being read. A client that stops sending must lose the slot after a
// while, not keep it until the TCP connection dies.
func TestStalledUploadBodyIsCutOff(t *testing.T) {
	old := bodyStall
	bodyStall = 300 * time.Millisecond
	defer func() { bodyStall = old }()

	type result struct {
		n   int64
		err error
	}
	done := make(chan result, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /upload/{id}/chunk/{n}", func(w http.ResponseWriter, r *http.Request) {
		n, err := io.Copy(io.Discard, r.Body)
		done <- result{n, err}
	})
	srv := httptest.NewServer(limitAPIBodies(mux))
	defer srv.Close()

	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "PUT /upload/abc/chunk/0 HTTP/1.1\r\nHost: x\r\nContent-Length: 1000\r\n\r\n0123456789")

	select {
	case got := <-done:
		if got.err == nil {
			t.Fatalf("the handler read %d bytes without an error from a body that stalled at 10 of 1000", got.n)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the handler is still waiting for a body that stopped arriving")
	}
}

// A body that keeps arriving is never cut, however long the whole upload takes.
func TestSlowButLiveUploadBodyIsNotCutOff(t *testing.T) {
	old := bodyStall
	bodyStall = 300 * time.Millisecond
	defer func() { bodyStall = old }()

	done := make(chan error, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /upload/{id}/chunk/{n}", func(w http.ResponseWriter, r *http.Request) {
		_, err := io.Copy(io.Discard, r.Body)
		done <- err
	})
	srv := httptest.NewServer(limitAPIBodies(mux))
	defer srv.Close()

	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "PUT /upload/abc/chunk/0 HTTP/1.1\r\nHost: x\r\nContent-Length: 10\r\n\r\n")
	for i := 0; i < 10; i++ { // 1 s in total, more than three stall windows
		time.Sleep(100 * time.Millisecond)
		fmt.Fprint(conn, "x")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("a body arriving a byte every 100 ms was cut: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the handler did not finish")
	}
}

// Once the body is in, the handler may work for as long as it needs (hashing and
// publishing a large file): the stall deadline must not cancel the request then.
func TestStallDeadlineEndsWithTheBody(t *testing.T) {
	old := bodyStall
	bodyStall = 200 * time.Millisecond
	defer func() { bodyStall = old }()

	done := make(chan error, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /upload/{id}/chunk/{n}", func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			done <- err
			return
		}
		time.Sleep(4 * bodyStall)
		done <- r.Context().Err()
	})
	srv := httptest.NewServer(limitAPIBodies(mux))
	defer srv.Close()

	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "PUT /upload/abc/chunk/0 HTTP/1.1\r\nHost: x\r\nContent-Length: 4\r\n\r\nbody")
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the request was cancelled after its body had been read: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the handler did not finish")
	}
}
