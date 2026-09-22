// Package fetchguard is the SSRF guard for all outbound HTTP fetches: URL
// validation plus a dialer that pins the check to the actually dialed IP.
// Extracted from internal/podcast so other subsystems (saved items) can share it.
package fetchguard

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// ErrBlocked is returned for URLs that fail the SSRF guard.
var ErrBlocked = errors.New("fetchguard: blocked URL")

// ValidateURL enforces http/https and rejects hosts that resolve to loopback,
// private, or link-local addresses. It is a cheap first pass; Dialer is the
// authoritative guard (it re-checks the IP actually dialed).
func ValidateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%w: parse: %v", ErrBlocked, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%w: scheme %q", ErrBlocked, u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("%w: empty host", ErrBlocked)
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		// Can't resolve (offline/unknown). Reject to be safe.
		return fmt.Errorf("%w: resolve %q: %v", ErrBlocked, host, err)
	}
	for _, ip := range ips {
		if isBlockedIP(ip) {
			return fmt.Errorf("%w: %s -> %s", ErrBlocked, host, ip)
		}
	}
	return nil
}

// isBlockedIP reports whether ip is loopback, private, link-local, or unspecified.
func isBlockedIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}

// Dialer re-checks the *resolved* IP at connection time (after DNS, before
// connect) and rejects blocked targets. ValidateURL alone is a TOCTOU gate: it
// resolves the host once, but http.Client re-resolves at dial time, so a DNS
// rebinding attacker can pass validation with a public IP then serve a private
// IP for the real request. Pinning the check to the actual dialed address closes
// that gap.
var Dialer = &net.Dialer{
	Timeout:   10 * time.Second,
	KeepAlive: 30 * time.Second,
	Control: func(_, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return fmt.Errorf("%w: dial address %q: %v", ErrBlocked, address, err)
		}
		ip := net.ParseIP(host)
		if ip == nil || isBlockedIP(ip) {
			return fmt.Errorf("%w: dial %s", ErrBlocked, address)
		}
		return nil
	},
}

// UserAgent is sent on all outbound fetches. A browser-like string: the server
// fetches pages as the user's agent on the user's explicit action, and sites
// like Wikipedia reject Go's default "Go-http-client" UA with a 403.
const UserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:128.0) Gecko/20100101 Firefox/128.0"

// uaTransport injects the User-Agent unless the caller already set one.
type uaTransport struct {
	inner http.RoundTripper
}

func (t uaTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", UserAgent)
	}
	return t.inner.RoundTrip(req)
}

// NewClient returns an SSRF-guarded HTTP client. timeout bounds the whole
// exchange; pass 0 for long transfers (large downloads) — the header phase is
// still bounded by ResponseHeaderTimeout and the caller's request context sets
// the overall deadline. Every redirect target is re-validated.
func NewClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: uaTransport{inner: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           Dialer.DialContext,
			ResponseHeaderTimeout: 30 * time.Second,
		}},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return ValidateURL(req.URL.String())
		},
	}
}

// WithCookie returns a per-download client. Never copy browser credentials to a
// different origin, and never downgrade a credential-bearing request to HTTP.
func WithCookie(client *http.Client, req *http.Request, cookie string) (*http.Client, error) {
	if req.URL.Scheme != "https" || req.URL.User != nil {
		return nil, fmt.Errorf("%w: cookies require HTTPS", ErrBlocked)
	}
	origin := *req.URL
	req.Header.Set("Cookie", cookie)
	copyClient := *client
	previous := client.CheckRedirect
	copyClient.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("too many redirects")
		}
		if next.URL.Scheme != "https" {
			return fmt.Errorf("%w: credential redirect requires HTTPS", ErrBlocked)
		}
		// net/http may restore initial headers on later redirects: explicitly remove
		// cookies after any cross-origin hop, even when the chain returns home.
		same := strings.EqualFold(next.URL.Host, origin.Host) && next.URL.User == nil
		for _, hop := range via {
			same = same && strings.EqualFold(hop.URL.Host, origin.Host) && hop.URL.Scheme == "https"
		}
		if !same {
			next.Header.Del("Cookie")
		}
		if previous != nil {
			return previous(next, via)
		}
		return nil
	}
	return &copyClient, nil
}
