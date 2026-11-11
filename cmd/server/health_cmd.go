package main

import (
	"net"
	"net/http"
	"time"
)

// runHealthcheck is `server healthcheck`: exit 0 when the server answers on its own
// port. It is what the container's HEALTHCHECK runs — the runtime image has no shell and
// no curl. Any answer short of a server error counts: behind a proxy the server refuses
// plain HTTP from anyone (403), which still says it is up and past its migrations.
func runHealthcheck(host, port string, timeout time.Duration) int {
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}
	client := &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get("http://" + net.JoinHostPort(host, port) + "/health")
	if err != nil {
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode >= http.StatusInternalServerError {
		return 1
	}
	return 0
}
