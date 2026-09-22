package storage

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
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

// Exercise nginx's actual file open, including a link introduced AFTER the Go
// authorization/redirect step. A Go-side path check alone cannot close this race.
func TestNginxRejectsSymlinks(t *testing.T) {
	testcontainers.SkipIfProviderIsNotHealthy(t)
	ctx := context.Background()
	raw, err := os.ReadFile("../../deploy/nginx/default.conf.template")
	if err != nil {
		t.Fatal(err)
	}
	template := string(raw)
	if strings.Count(template, "disable_symlinks on;") != 1 {
		t.Fatal("TLS file serving must disable symlinks")
	}
	config := strings.ReplaceAll(template, "http://app:${APP_PORT}", "http://127.0.0.1:8081")
	config = strings.Replace(config, "    listen 443 ssl;", `    listen 443 ssl;
    location /probe/ { rewrite ^/probe/(.*)$ /__data/$1 last; }
    location /control/ { alias /data/; disable_symlinks off; }`, 1)
	config += `
 server { listen 8081; location / { add_header X-Test-Forwarded-Proto $http_x_forwarded_proto always; add_header X-Test-Forwarded-For $http_x_forwarded_for always; return 200 "backend"; } }
 `
	cert, key, roots := nginxTestCertificate(t)
	conf := filepath.Join(t.TempDir(), "default.conf")
	if err := os.WriteFile(conf, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: "nginx:alpine", ExposedPorts: []string{"80/tcp", "443/tcp"},
			Files: []testcontainers.ContainerFile{
				{HostFilePath: conf, ContainerFilePath: "/etc/nginx/conf.d/default.conf", FileMode: 0644},
				{HostFilePath: cert, ContainerFilePath: "/etc/nginx/certs/dev.pem", FileMode: 0644},
				{HostFilePath: key, ContainerFilePath: "/etc/nginx/certs/dev-key.pem", FileMode: 0600},
			},
			Cmd:        []string{"sh", "-c", `mkdir -p /data/alice /data/bob /outside; printf public > /data/alice/ordinary; printf private > /data/bob/secret; printf outside > /outside/secret; chmod -R a+rX /data /outside; exec nginx -g 'daemon off;'`},
			WaitingFor: wait.ForHTTP("/").WithPort("80/tcp").WithStatusCodeMatcher(func(code int) bool { return code == http.StatusForbidden }).WithStartupTimeout(45 * time.Second),
		}, Started: true,
	})
	if err != nil {
		t.Fatalf("start nginx: %v", err)
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
	base := "https://" + net.JoinHostPort(host, port.Port())
	// Simulate the filesystem change after an authorized path has been selected.
	code, _, err := c.Exec(ctx, []string{"sh", "-c", `ln -s ../bob/secret /data/alice/filelink; ln -s ../bob /data/alice/dirlink; ln -s /outside/secret /data/alice/external`})
	if err != nil || code != 0 {
		t.Fatalf("create links: exit=%d err=%v", code, err)
	}
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "localhost"}}}
	t.Cleanup(func() { client.CloseIdleConnections() })
	plainPort, err := c.MappedPort(ctx, "80/tcp")
	if err != nil {
		t.Fatal(err)
	}
	plainBase := "http://" + net.JoinHostPort(host, plainPort.Port())
	for _, method := range []string{"GET", "POST", "PUT", "PROPFIND"} {
		for _, path := range []string{"/login", "/dav/file", "/files/id/content", "/rest/stream?apiKey=secret", "/__data/alice/ordinary"} {
			req, err := http.NewRequest(method, plainBase+path, strings.NewReader("password=secret"))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Basic c2VjcmV0")
			req.Header.Set("X-Forwarded-Proto", "https")
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != 403 || resp.Header.Get("Location") != "" || resp.Header.Get("X-Test-Forwarded-Proto") != "" || strings.Contains(string(body), "backend") {
				t.Fatalf("HTTP reached backend: %s %s %d %q", method, path, resp.StatusCode, body)
			}
		}
	}
	req, _ := http.NewRequest("GET", base+"/login", nil)
	req.Header.Set("X-Forwarded-Proto", "http")
	req.Header.Set("X-Forwarded-For", "203.0.113.199")
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 200 || response.Header.Get("X-Test-Forwarded-Proto") != "https" || response.Header.Get("Strict-Transport-Security") == "" || response.Header.Get("X-Test-Forwarded-For") == "203.0.113.199" {
		t.Fatalf("TLS proxy failed: %d %v", response.StatusCode, response.Header)
	}
	for _, path := range []string{"alice/ordinary", "alice/filelink", "alice/dirlink/secret", "alice/external"} {
		resp, err := client.Get(base + "/probe/" + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if path == "alice/ordinary" {
			if resp.StatusCode != 200 || string(body) != "public" {
				t.Fatalf("ordinary delivery: %d %q", resp.StatusCode, body)
			}
		} else if (resp.StatusCode != 403 && resp.StatusCode != 404) || strings.Contains(string(body), "private") {
			t.Fatalf("link delivery: %s %d %q", path, resp.StatusCode, body)
		}
	}
	// Control proves those same targets exist and nginx would otherwise follow them.
	for _, path := range []string{"alice/filelink", "alice/dirlink/secret"} {
		resp, err := client.Get(base + "/control/" + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || string(body) != "private" {
			t.Fatalf("invalid control fixture: %d %q", resp.StatusCode, body)
		}
	}
	resp, err := client.Get(base + "/__data/alice/ordinary")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("internal location exposed: %d", resp.StatusCode)
	}
}

// A fresh self-signed test CA avoids relying on production certificates or disabling TLS verification.
func nginxTestCertificate(t *testing.T) (string, string, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"}, DNSNames: []string{"localhost"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certPath, certPEM, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0600); err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(certPEM) {
		t.Fatal("invalid test certificate")
	}
	return certPath, keyPath, roots
}
