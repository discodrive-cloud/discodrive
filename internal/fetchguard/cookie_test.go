package fetchguard

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

type cookieTransport func(*http.Request) (*http.Response, error)

func (f cookieTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCookieRedirectBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name       string
		redirects  []string
		wantCookie string
		wantError  bool
	}{
		{"same origin", []string{"https://example.test/next"}, "secret=test", false},
		{"downgrade", []string{"http://example.test/next"}, "", true},
		{"subdomain", []string{"https://child.example.test/next"}, "", false},
		{"port", []string{"https://example.test:8443/next"}, "", false},
		{"return home", []string{"https://child.example.test/next", "https://example.test/final"}, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := 0
			base := &http.Client{Transport: cookieTransport(func(r *http.Request) (*http.Response, error) {
				if n < len(tc.redirects) {
					target := tc.redirects[n]
					n++
					return &http.Response{StatusCode: 302, Header: http.Header{"Location": {target}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
				}
				if tc.wantError {
					t.Error("unsafe redirect reached transport")
				}
				if got := r.Header.Get("Cookie"); got != tc.wantCookie {
					t.Errorf("cookie=%q", got)
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("ok")), Request: r}, nil
			})}
			req, _ := http.NewRequest("GET", "https://example.test/start", nil)
			client, err := WithCookie(base, req, "secret=test")
			if err != nil {
				t.Fatal(err)
			}
			resp, err := client.Do(req)
			if (err != nil) != tc.wantError {
				t.Fatalf("err=%v", err)
			}
			if resp != nil {
				resp.Body.Close()
			}
		})
	}
	req, _ := http.NewRequest("GET", "http://example.test/start", nil)
	if _, err := WithCookie(http.DefaultClient, req, "secret=test"); err == nil {
		t.Fatal("HTTP credentials accepted")
	}
}
