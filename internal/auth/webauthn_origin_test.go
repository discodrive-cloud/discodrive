package auth

import (
	"slices"
	"testing"
)

// The default install is BASE_DOMAIN=localhost with HTTPS published on 8443: the browser's
// origin is https://localhost:8443, and with only https://localhost expected every
// security key was refused ("could not verify the authenticator").
func TestNewWebAuthn_AcceptsThePublishedHTTPSPort(t *testing.T) {
	cases := []struct {
		domain, port string
		want         []string
	}{
		{"localhost", "8443", []string{"https://localhost", "https://localhost:8443"}},
		{"localhost", "443", []string{"https://localhost"}},
		{"localhost", "", []string{"https://localhost"}},
		// a port written in BASE_DOMAIN is the operator's own answer
		{"localhost:9443", "8443", []string{"https://localhost:9443"}},
		{"https://disco.example.com", "8443", []string{"https://disco.example.com", "https://disco.example.com:8443"}},
	}
	for _, c := range cases {
		wa, err := NewWebAuthn(c.domain, c.port)
		if err != nil || wa == nil {
			t.Fatalf("NewWebAuthn(%q, %q): %v", c.domain, c.port, err)
		}
		if got := wa.Config.RPOrigins; !slices.Equal(got, c.want) {
			t.Errorf("NewWebAuthn(%q, %q) origins = %v, want %v", c.domain, c.port, got, c.want)
		}
		if wa.Config.RPID != "localhost" && wa.Config.RPID != "disco.example.com" {
			t.Errorf("RPID = %q: must be the bare host", wa.Config.RPID)
		}
	}
}
