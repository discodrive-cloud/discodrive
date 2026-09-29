package fetchguard

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestValidateURLScheme(t *testing.T) {
	if err := ValidateURL("ftp://example.com/x"); err == nil {
		t.Error("ftp should be rejected")
	}
	if err := ValidateURL("file:///etc/passwd"); err == nil {
		t.Error("file scheme should be rejected")
	}
}

func TestValidateURLPrivate(t *testing.T) {
	for _, u := range []string{
		"http://127.0.0.1/feed",
		"http://localhost/feed",
		"http://169.254.169.254/latest/meta-data",
		"http://10.0.0.5/feed",
		"http://192.168.1.1/feed",
	} {
		if err := ValidateURL(u); err == nil {
			t.Errorf("%s should be rejected as private/loopback/link-local", u)
		}
	}
}

func TestValidateURLPublicLiteral(t *testing.T) {
	// 8.8.8.8 is a public IP literal; net.LookupIP returns it directly without
	// DNS, so the result is deterministic even in offline environments.
	if err := ValidateURL("https://8.8.8.8/feed.xml"); err != nil {
		t.Errorf("public IP literal should be accepted: %v", err)
	}
}

func TestNewClientBlocksLoopbackDial(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	_, err := NewClient(0).Get(srv.URL)
	if !errors.Is(err, ErrBlocked) {
		t.Errorf("dial to 127.0.0.1 should be blocked by the dialer, got %v", err)
	}
}

// Special-purpose ranges beyond RFC1918/loopback: CGNAT (Tailscale tailnets),
// benchmarking, reserved, NAT64 and documentation space must all be refused, and
// IPv4-mapped IPv6 must not smuggle a blocked IPv4 past the check.
func TestIsBlockedIPSpecialRanges(t *testing.T) {
	blocked := []string{
		"0.0.0.0", "0.1.2.3", "100.64.0.1", "100.100.100.100", "100.127.255.254",
		"192.0.0.8", "198.18.0.1", "198.19.255.255", "240.0.0.1", "255.255.255.255",
		"192.0.2.1", "224.0.0.251", "::", "::1", "::a00:1", "64:ff9b::a00:1",
		"64:ff9b::808:808", "2001:db8::1", "fc00::1", "fe80::1", "ff02::1",
		"::ffff:100.64.0.1", "::ffff:127.0.0.1", "::ffff:10.0.0.1",
	}
	for _, s := range blocked {
		if !isBlockedIP(net.ParseIP(s)) {
			t.Errorf("%s must be blocked", s)
		}
	}
	allowed := []string{"8.8.8.8", "1.1.1.1", "100.63.255.255", "100.128.0.1", "198.20.0.1", "2606:4700::1111", "::ffff:8.8.8.8"}
	for _, s := range allowed {
		if isBlockedIP(net.ParseIP(s)) {
			t.Errorf("%s is public and must be allowed", s)
		}
	}
	if err := ValidateURL("http://100.101.102.103/feed"); err == nil {
		t.Error("a tailnet (CGNAT) literal must be rejected by ValidateURL")
	}
}

// With HTTP(S)_PROXY set the transport would dial the proxy, and the dial-time IP
// check would only ever see the proxy's address: the guarded client must not use one.
func TestNewClientIgnoresEnvironmentProxy(t *testing.T) {
	tr, ok := NewClient(0).Transport.(uaTransport)
	if !ok {
		t.Fatalf("unexpected transport %T", NewClient(0).Transport)
	}
	if tr.inner.(*http.Transport).Proxy != nil {
		t.Fatal("guarded transport has a Proxy func: it bypasses the dial-time IP check")
	}
}
