package api

import (
	"crypto/tls"
	"discodrive/internal/httpsecurity"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientIPProxyBoundary(t *testing.T) {
	p, err := httpsecurity.New("10.0.0.2/32", "", false)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, peer string
		xff        []string
		want       string
	}{
		{"trusted", "10.0.0.2:5000", []string{"203.0.113.8"}, "203.0.113.8"},
		{"untrusted private", "10.0.0.3:5000", []string{"203.0.113.8"}, "10.0.0.3"},
		{"untrusted public", "198.51.100.1:5000", []string{"203.0.113.8"}, "198.51.100.1"},
		{"untrusted loopback", "127.0.0.1:5000", []string{"203.0.113.8"}, "127.0.0.1"},
		{"list rejected", "10.0.0.2:5000", []string{"203.0.113.8, 203.0.113.9"}, "10.0.0.2"},
		{"duplicates rejected", "10.0.0.2:5000", []string{"203.0.113.8", "203.0.113.9"}, "10.0.0.2"},
		{"invalid", "10.0.0.2:5000", []string{"anything"}, "10.0.0.2"},
		{"zone rejected", "10.0.0.2:5000", []string{"fe80::1%eth0"}, "10.0.0.2"},
		{"canonical IPv6", "10.0.0.2:5000", []string{"2001:0db8:0:0:0:0:0:1"}, "2001:db8::1"},
		{"mapped IPv4", "10.0.0.2:5000", []string{"::ffff:203.0.113.8"}, "203.0.113.8"},
		{"missing", "10.0.0.2:5000", nil, "10.0.0.2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "https://example.test/", nil)
			r.RemoteAddr = tc.peer
			r.TLS = &tls.ConnectionState{}
			for _, v := range tc.xff {
				r.Header.Add("X-Forwarded-For", v)
			}
			r.Header.Set("CF-Connecting-IP", "192.0.2.99")
			p.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if got := clientIP(r); got != tc.want {
					t.Fatalf("got %q want %q", got, tc.want)
				}
			})).ServeHTTP(httptest.NewRecorder(), r)
		})
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.0.2:123"
	r.Header.Set("X-Forwarded-For", "203.0.113.8")
	if got := clientIP(r); got != "10.0.0.2" {
		t.Fatalf("no transport policy trusted XFF: %s", got)
	}
}

func TestLoginLimitsSeparateTunnelClients(t *testing.T) {
	p, err := httpsecurity.New("10.0.0.2/32", "", false)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{loginLimiter: newLoginLimiter()}
	h := p.Handler(s.rateLimited(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	request := func(ip string) int {
		r := httptest.NewRequest("POST", "/auth/login", nil)
		r.RemoteAddr = "10.0.0.2:1234"
		r.Header.Set("X-Forwarded-Proto", "https")
		r.Header.Set("X-Forwarded-For", ip)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	for i := 0; i < 10; i++ {
		if got := request("203.0.113.8"); got != 204 {
			t.Fatal(got)
		}
	}
	if got := request("203.0.113.8"); got != 429 {
		t.Fatalf("exhausted client=%d", got)
	}
	if got := request("::ffff:203.0.113.8"); got != 429 {
		t.Fatalf("IP spelling bypass=%d", got)
	}
	if got := request("203.0.113.9"); got != 204 {
		t.Fatalf("other tunnel client blocked=%d", got)
	}
}
