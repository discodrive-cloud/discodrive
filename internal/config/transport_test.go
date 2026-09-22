package config

import (
	"discodrive/internal/httpsecurity"
	"testing"
)

func TestTransportDefaults(t *testing.T) {
	for _, name := range []string{"APP_HOST", "APP_PORT", "TRUSTED_PROXY_CIDRS", "ALLOW_INSECURE_HTTP"} {
		t.Setenv(name, "")
	}
	c := Load()
	if c.Addr() != "127.0.0.1:8080" || c.AllowInsecureHTTP || c.TrustedProxyCIDRs != httpsecurity.DefaultTrustedProxies {
		t.Fatalf("unsafe defaults: %s %t %q", c.Addr(), c.AllowInsecureHTTP, c.TrustedProxyCIDRs)
	}
	t.Setenv("APP_HOST", "::1")
	if got := Load().Addr(); got != "[::1]:8080" {
		t.Fatalf("IPv6 addr: %s", got)
	}
	t.Setenv("ALLOW_INSECURE_HTTP", "typo")
	if Load().AllowInsecureHTTP {
		t.Fatal("invalid config enabled HTTP")
	}
}
