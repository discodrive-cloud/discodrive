package api

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"discodrive"
	"discodrive/internal/auth"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSetupWithoutTokenCannotCreateAdmin(t *testing.T) {
	_, q, svc := bootstrapPairingDB(t)
	s := &Server{auth: svc, q: q}
	req := httptest.NewRequest("POST", "https://drive.test/setup/admin", strings.NewReader(`{"email":"intruder@example.test","password":"long-password"}`))
	rec := httptest.NewRecorder()
	s.handleSetupAdmin(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("setup without secret returned %d", rec.Code)
	}
	count, err := q.CountAdmins(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("unauthenticated administrator created")
	}
}

func TestBootstrapLifecycleAndConcurrentRequests(t *testing.T) {
	ctx := context.Background()
	pool, q, svc := bootstrapPairingDB(t)
	path := filepath.Join(t.TempDir(), "private", "setup-token")
	if err := svc.InitBootstrap(ctx, path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	token := strings.TrimSpace(string(data))
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatal("token file is not private")
	}
	state, err := q.GetBootstrap(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.TokenHash == token || state.TokenHash != auth.TokenHash(token) {
		t.Fatal("expected only a digest in database")
	}
	// A process restart must reuse the pending token, never disclose it via status.
	other := auth.NewService(pool, auth.NewTokenIssuer("secret", time.Hour), nil)
	if err := other.InitBootstrap(ctx, path); err != nil {
		t.Fatal(err)
	}
	again, _ := os.ReadFile(path)
	if string(again) != string(data) {
		t.Fatal("restart rotated a live token")
	}
	s := &Server{auth: svc, q: q}
	status := httptest.NewRecorder()
	s.handleSetupStatus(status, httptest.NewRequest("GET", "https://drive.test/setup/status", nil))
	if status.Code != 200 || strings.Contains(status.Body.String(), token) || strings.Contains(status.Body.String(), state.TokenHash) {
		t.Fatal("status disclosed a secret")
	}
	request := func(email, tok, target string, remote, proto string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{"email": email, "password": "long-password", "token": tok})
		req := httptest.NewRequest("POST", target, bytes.NewReader(body))
		if remote != "" {
			req.RemoteAddr = remote
		}
		if proto != "" {
			req.Header.Set("X-Forwarded-Proto", proto)
		}
		rec := httptest.NewRecorder()
		s.handleSetupAdmin(rec, req)
		return rec
	}
	if rec := request("wrong@example.test", strings.Repeat("0", 64), "https://drive.test/setup/admin", "", ""); rec.Code != 401 {
		t.Fatalf("invalid token: %d", rec.Code)
	}
	for _, proto := range []string{"", "https"} {
		if rec := request("http@example.test", token, "http://drive.test/setup/admin", "203.0.113.2:1234", proto); rec.Code != 403 {
			t.Fatal("insecure or spoofed HTTPS accepted")
		}
	}
	// The real proxy contract is accepted. Both requests start with the same secret.
	start := make(chan struct{})
	results := make(chan int, 2)
	for _, email := range []string{"one@example.test", "two@example.test"} {
		go func(email string) {
			<-start
			results <- request(email, token, "http://drive.test/setup/admin", "127.0.0.1:1234", "https").Code
		}(email)
	}
	close(start)
	codes := map[int]int{}
	for i := 0; i < 2; i++ {
		codes[<-results]++
	}
	if codes[201] != 1 || codes[409] != 1 {
		t.Fatalf("concurrent setup results: %v", codes)
	}
	count, err := q.CountAdmins(ctx)
	if err != nil || count != 1 {
		t.Fatalf("admin count: %d %v", count, err)
	}
	state, err = q.GetBootstrap(ctx)
	if err != nil || !state.Completed || state.TokenHash != "" {
		t.Fatal("setup did not permanently consume token")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("setup token file survived completion")
	}
	// Delete the last administrator: neither status nor a restart may reopen setup.
	if _, err := pool.Exec(ctx, "DELETE FROM users WHERE role='admin'"); err != nil {
		t.Fatal(err)
	}
	if needed, err := svc.SetupNeeded(ctx); err != nil || needed {
		t.Fatal("last-admin deletion reopened setup")
	}
	if err := other.InitBootstrap(ctx, path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("closed setup recreated token")
	}
	if rec := request("replacement@example.test", token, "https://drive.test/setup/admin", "", ""); rec.Code != 409 {
		t.Fatalf("replay after deletion: %d", rec.Code)
	}
}

func TestBootstrapFailedTransactionDoesNotConsumeToken(t *testing.T) {
	ctx := context.Background()
	_, q, svc := bootstrapPairingDB(t)
	path := filepath.Join(t.TempDir(), "setup-token")
	if err := svc.InitBootstrap(ctx, path); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	token := strings.TrimSpace(string(raw))
	if _, _, err := svc.Register(ctx, "existing@example.test", "long-password"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetupAdmin(ctx, "existing@example.test", "long-password", token); err == nil {
		t.Fatal("duplicate account accepted")
	}
	state, err := q.GetBootstrap(ctx)
	if err != nil || state.Completed || state.TokenHash == "" {
		t.Fatal("failed setup consumed token")
	}
	if _, err := svc.SetupAdmin(ctx, "admin@example.test", "long-password", token); err != nil {
		t.Fatal(err)
	}
}

func TestBootstrapMigrationClosesExistingInstallation(t *testing.T) {
	ctx := context.Background()
	pool, q, svc := bootstrapPairingDB(t)
	_, user, err := svc.Register(ctx, "existing@example.test", "long-password")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "DROP TABLE server_bootstrap"); err != nil {
		t.Fatal(err)
	}
	migration, err := discodrive.Migrations.ReadFile("migrations/000010_bootstrap_security.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, string(migration)); err != nil {
		t.Fatal(err)
	}
	state, err := q.WithTx(tx).GetBootstrap(ctx)
	if err != nil || !state.Completed || state.TokenHash != "" {
		t.Fatal("existing installation reopened by migration")
	}
	if _, err := tx.Exec(ctx, "DELETE FROM users WHERE id=$1", user.ID); err != nil {
		t.Fatal(err)
	}
	state, err = q.WithTx(tx).GetBootstrap(ctx)
	if err != nil || !state.Completed {
		t.Fatal("completion tied to users")
	}
}
