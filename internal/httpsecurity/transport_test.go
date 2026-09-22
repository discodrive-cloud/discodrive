package httpsecurity

import (
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type unreadBody struct{}

func (unreadBody) Read([]byte) (int, error) { panic("insecure request body was read") }
func (unreadBody) Close() error             { return nil }

func TestHTTPSBoundary(t *testing.T) {
	p, err := New("127.0.0.1/32,::1/128,172.20.0.2/32", "127.0.0.1", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name, remote, proto string
		tls, ok             bool
	}{
		{"plain public", "203.0.113.1:456", "", false, false},
		{"spoofed public", "203.0.113.1:456", "https", false, false},
		{"untrusted private", "192.168.1.5:456", "https", false, false},
		{"proxy plain", "172.20.0.2:456", "http", false, false},
		{"proxy TLS", "172.20.0.2:456", "https", false, true},
		{"loopback plain", "127.0.0.1:456", "", false, false},
		{"loopback TLS", "127.0.0.1:456", "https", false, true},
		{"IPv6 proxy", "[::1]:456", "https", false, true},
		{"mapped proxy", "[::ffff:127.0.0.1]:456", "https", false, true},
		{"ambiguous chain", "127.0.0.1:456", "https,http", false, false},
		{"native TLS", "203.0.113.1:456", "http", true, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for _, path := range []string{"/login", "/dav/private", "/rest/stream?apiKey=secret", "/files/id/content", "/app/", "/setup/admin"} {
				called := false
				h := p.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					called = true
					if !IsHTTPS(r) {
						t.Error("verified TLS not propagated")
					}
					w.WriteHeader(204)
				}))
				r := httptest.NewRequest("POST", path, io.NopCloser(strings.NewReader("password")))
				r.Body = unreadBody{}
				r.RemoteAddr = tt.remote
				r.Header.Set("X-Forwarded-Proto", tt.proto)
				r.Header.Set("X-Forwarded-For", "127.0.0.1")
				r.Header.Set("Authorization", "Basic c2VjcmV0")
				if tt.tls {
					r.TLS = &tls.ConnectionState{}
				}
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if called != tt.ok {
					t.Fatalf("%s handler reached=%t want=%t", path, called, tt.ok)
				}
				if tt.ok {
					if w.Code != 204 || w.Header().Get("Strict-Transport-Security") == "" {
						t.Fatal("missing HTTPS response/HSTS")
					}
				} else if w.Code != 403 || w.Header().Get("Location") != "" || w.Header().Get("WWW-Authenticate") != "" || w.Header().Get("Cache-Control") != "no-store" {
					t.Fatalf("insecure response: %d %v", w.Code, w.Header())
				}
			}
		})
	}
}

func TestDuplicateForwardedProtoRejected(t *testing.T) {
	p, _ := New(DefaultTrustedProxies, "127.0.0.1", false)
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:123"
	r.Header.Add("X-Forwarded-Proto", "https")
	r.Header.Add("X-Forwarded-Proto", "http")
	p.Handler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("duplicate TLS assertion accepted") })).ServeHTTP(httptest.NewRecorder(), r)
}

func TestLocalHTTPException(t *testing.T) {
	for _, host := range []string{"", "0.0.0.0", "::", "192.168.1.1", "localhost"} {
		if _, err := New(DefaultTrustedProxies, host, true); err == nil {
			t.Fatalf("insecure listen host accepted: %q", host)
		}
	}
	p, err := New(DefaultTrustedProxies, "127.0.0.1", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, remote := range []string{"127.0.0.1:123", "203.0.113.1:123"} {
		called := false
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = remote
		w := httptest.NewRecorder()
		p.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called = true
			if IsHTTPS(r) {
				t.Error("HTTP marked as secure")
			}
		})).ServeHTTP(w, r)
		if called != (remote == "127.0.0.1:123") {
			t.Fatalf("dev bypass: %s", remote)
		}
		if w.Header().Get("Strict-Transport-Security") != "" {
			t.Fatal("HTTP sent HSTS")
		}
	}
}

func TestProxyConfigurationRejectsInvalidOrGlobalTrust(t *testing.T) {
	for _, cidrs := range []string{"invalid", "0.0.0.0/0", "::/0"} {
		if _, err := New(cidrs, "127.0.0.1", false); err == nil {
			t.Fatalf("accepted proxy config: %s", cidrs)
		}
	}
}
