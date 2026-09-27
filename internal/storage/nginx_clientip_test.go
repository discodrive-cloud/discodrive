package storage

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// Loopback listener 444 simulates nginx reached from a trusted tunnel peer.
// The published listener trusts no TCP peer and must ignore direct spoofing.
func TestNginxTunnelClientIP(t *testing.T) {
	testcontainers.SkipIfProviderIsNotHealthy(t)
	ctx := context.Background()
	raw, err := os.ReadFile("../../deploy/nginx/default.conf.template")
	if err != nil {
		t.Fatal(err)
	}
	base := strings.ReplaceAll(string(raw), "http://app:${APP_PORT}", "http://127.0.0.1:8081")
	public := strings.ReplaceAll(base, "${NGINX_CLOUDFLARED_PEER}", "unix:")
	public = strings.Replace(public, "    listen 443 ssl;", `    listen 443 ssl;
    location /via-tunnel/ {
        proxy_pass https://127.0.0.1:444/;
        proxy_ssl_verify on;
        proxy_ssl_trusted_certificate /etc/nginx/certs/dev.pem;
        proxy_ssl_name localhost;
    }`, 1)
	trusted := base[strings.Index(base, "server {\n    listen 443 ssl;"):]
	trusted = strings.Replace(trusted, "listen 443 ssl;", "listen 127.0.0.1:444 ssl;", 1)
	trusted = strings.ReplaceAll(trusted, "${NGINX_CLOUDFLARED_PEER}", "127.0.0.1/32")
	config := public + trusted + `server { listen 127.0.0.1:8081; location / { default_type text/plain; return 200 "$http_x_forwarded_for"; } }`
	conf := filepath.Join(t.TempDir(), "default.conf")
	if err := os.WriteFile(conf, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	cert, key, roots := nginxTestCertificate(t)
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: testcontainers.ContainerRequest{
		Image: "nginx:alpine", ExposedPorts: []string{"443/tcp"},
		Files: []testcontainers.ContainerFile{
			{HostFilePath: conf, ContainerFilePath: "/etc/nginx/conf.d/default.conf", FileMode: 0644},
			{HostFilePath: cert, ContainerFilePath: "/etc/nginx/certs/dev.pem", FileMode: 0644},
			{HostFilePath: key, ContainerFilePath: "/etc/nginx/certs/dev-key.pem", FileMode: 0600},
		}, WaitingFor: wait.ForListeningPort("443/tcp").WithStartupTimeout(45 * time.Second),
	}, Started: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Terminate(ctx) })
	host, err := c.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := c.MappedPort(ctx, "443/tcp")
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "localhost"}}}
	t.Cleanup(client.CloseIdleConnections)
	for _, tc := range []struct{ path, cf, want string }{
		{"/via-tunnel/auth/login", "203.0.113.8", "203.0.113.8"},
		{"/via-tunnel/auth/login", "203.0.113.9", "203.0.113.9"},
		{"/via-tunnel/dav/", "2001:db8::1", "2001:db8::1"},
		{"/via-tunnel/sync/events", "203.0.113.8", "203.0.113.8"},
		{"/via-tunnel/auth/login", "not-an-ip", "127.0.0.1"},
		{"/via-tunnel/auth/login", "", "127.0.0.1"},
		{"/auth/login", "203.0.113.8", ""},
	} {
		t.Run(tc.path+tc.cf, func(t *testing.T) {
			r, _ := http.NewRequest("GET", "https://"+net.JoinHostPort(host, port.Port())+tc.path, nil)
			r.Header.Set("CF-Connecting-IP", tc.cf)
			r.Header.Set("X-Forwarded-For", "192.0.2.199")
			r.Header.Set("X-Real-IP", "192.0.2.199")
			res, err := client.Do(r)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(res.Body)
			res.Body.Close()
			if res.StatusCode != 200 {
				t.Fatalf("status=%d body=%s", res.StatusCode, body)
			}
			got := string(body)
			if tc.want != "" && got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
			if tc.want == "" && (got == tc.cf || got == "192.0.2.199" || net.ParseIP(got) == nil) {
				t.Fatalf("direct header trusted: %q", got)
			}
		})
	}
}
