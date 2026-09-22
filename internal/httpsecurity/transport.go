// Package httpsecurity enforces the public transport boundary before routing or authentication.
package httpsecurity

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

const DefaultTrustedProxies = "127.0.0.1/32,::1/128"

type Policy struct {
	proxies        []netip.Prefix
	allowLocalHTTP bool
}
type verifiedHTTPSKey struct{}

// New validates the proxy allowlist and keeps the development exception local.
func New(cidrs, listenHost string, allowLocalHTTP bool) (*Policy, error) {
	if allowLocalHTTP {
		ip, err := netip.ParseAddr(listenHost)
		if err != nil || !ip.IsLoopback() {
			return nil, fmt.Errorf("ALLOW_INSECURE_HTTP requires an explicit loopback APP_HOST")
		}
	}
	p := &Policy{allowLocalHTTP: allowLocalHTTP}
	for _, raw := range strings.Split(cidrs, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid TRUSTED_PROXY_CIDRS entry %q: %w", raw, err)
		}
		if prefix.Bits() == 0 {
			return nil, fmt.Errorf("TRUSTED_PROXY_CIDRS must not trust all addresses")
		}
		p.proxies = append(p.proxies, prefix.Masked())
	}
	return p, nil
}

func peerIP(r *http.Request) netip.Addr {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}
	}
	ip, _ := netip.ParseAddr(host)
	return ip.Unmap()
}

func (p *Policy) isHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	// Never trust X-Forwarded-For to decide which peer may assert TLS.
	values := r.Header.Values("X-Forwarded-Proto")
	if len(values) != 1 || values[0] != "https" {
		return false
	}
	ip := peerIP(r)
	for _, prefix := range p.proxies {
		if prefix.Contains(ip) {
			return true
		}
	}
	return false
}

// IsHTTPS lets setup reuse the outer transport decision. Standalone handlers
// (including tests) trust only loopback proxies unless a policy verified it.
func IsHTTPS(r *http.Request) bool {
	if secure, ok := r.Context().Value(verifiedHTTPSKey{}).(bool); ok {
		return secure
	}
	p, _ := New(DefaultTrustedProxies, "", false)
	return p.isHTTPS(r)
}

func (p *Policy) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secure := p.isHTTPS(r)
		if !secure && !(p.allowLocalHTTP && peerIP(r).IsLoopback()) {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			// No redirects: credentials and token-bearing URLs must not be replayed.
			http.Error(w, "Use HTTPS to access this server.", http.StatusForbidden)
			return
		}
		if secure {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), verifiedHTTPSKey{}, secure)))
	})
}
