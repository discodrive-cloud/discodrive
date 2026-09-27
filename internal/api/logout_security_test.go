package api

import (
	"context"
	"discodrive/internal/auth"
	"discodrive/internal/db"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestBrowserLogoutRevokesRenewalsAndStreams(t *testing.T) {
	ctx := context.Background()
	pool, q, svc := bootstrapPairingDB(t)
	token, u, err := svc.Register(ctx, "logout@example.test", "password12")
	if err != nil {
		t.Fatal(err)
	}
	other, err := svc.Login(ctx, u.Email, "password12")
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{auth: svc, q: q}
	request := func(service *auth.Service, token string, handler http.HandlerFunc) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/auth/logout", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		service.Middleware(handler).ServeHTTP(w, r)
		return w
	}
	probe := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	uid := db.UUIDString(u.ID)
	sessionCtx := streamSessionContext(t, svc, token)
	mint, err := svc.StreamMinter(sessionCtx, uid)
	if err != nil {
		t.Fatal(err)
	}
	media, err := mint("track")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ValidateStreamToken(ctx, media, "track"); err != nil {
		t.Fatal(err)
	}
	renewal := request(svc, token, probe).Header().Get("X-Token")
	if renewal == "" {
		t.Fatal("missing renewal")
	}
	// Capture an already-authorized request and let it finish only after logout.
	entered := make(chan struct{})
	release := make(chan struct{})
	late := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		late <- request(svc, token, func(w http.ResponseWriter, r *http.Request) { close(entered); <-release; w.WriteHeader(204) })
	}()
	<-entered
	logout := request(svc, token, server.handleLogout)
	close(release)
	lateResponse := <-late
	if logout.Code != 204 || logout.Header().Get("X-Token") != "" {
		t.Fatalf("logout=%d headers=%v", logout.Code, logout.Header())
	}
	second := auth.NewService(pool, issuerForSecurityTest(), nil)
	for _, stolen := range []string{token, renewal, lateResponse.Header().Get("X-Token")} {
		if w := request(second, stolen, probe); w.Code != 401 || w.Header().Get("X-Token") != "" {
			t.Fatalf("revoked token accepted=%d", w.Code)
		}
	}
	if _, err := second.ValidateStreamToken(ctx, media, "track"); err == nil {
		t.Fatal("stream survived logout")
	}
	if _, err := svc.StreamMinter(sessionCtx, uid); err == nil {
		t.Fatal("stale request minted new stream")
	}
	if w := request(second, other.Token, probe); w.Code != 204 {
		t.Fatalf("other browser session revoked=%d", w.Code)
	}
	legacy, err := issuerForSecurityTest().Issue(uid, db.UUIDString(u.TenantID), u.Role, u.TokenVersion, "")
	if err != nil {
		t.Fatal(err)
	}
	if w := request(second, legacy, probe); w.Code != 401 {
		t.Fatal("legacy browser token accepted")
	}
	missing, err := issuerForSecurityTest().IssueTTL(uid, db.UUIDString(u.TenantID), u.Role, u.TokenVersion, "", time.Hour, "nonexistent-session")
	if err != nil {
		t.Fatal(err)
	}
	if w := request(second, missing, probe); w.Code != 401 {
		t.Fatal("nonexistent server session accepted")
	}
	legacyMedia, err := issuerForSecurityTest().IssueStream(uid, "track", u.TokenVersion, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.ValidateStreamToken(ctx, legacyMedia, "track"); err == nil {
		t.Fatal("legacy browser stream accepted")
	}
}

func TestBrowserLogoutWithExpiredTokenRevokesStreams(t *testing.T) {
	ctx := context.Background()
	_, q, svc := bootstrapPairingDB(t)
	token, u, err := svc.Register(ctx, "expired-logout@example.test", "password12")
	if err != nil {
		t.Fatal(err)
	}
	mint, err := svc.StreamMinter(streamSessionContext(t, svc, token), db.UUIDString(u.ID))
	if err != nil {
		t.Fatal(err)
	}
	media, err := mint("track")
	if err != nil {
		t.Fatal(err)
	}
	claims, err := issuerForSecurityTest().Parse(token)
	if err != nil {
		t.Fatal(err)
	}
	// The browser's idle TTL can expire before a previously issued media URL.
	claims.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-time.Minute))
	expired, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{auth: svc, q: q}
	r := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	r.Header.Set("Authorization", "Bearer "+expired)
	w := httptest.NewRecorder()
	svc.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("expired browser token authorized an ordinary request")
	})).ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("ordinary request status=%d", w.Code)
	}
	w = httptest.NewRecorder()
	server.handleLogout(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("logout status=%d: %s", w.Code, w.Body.String())
	}
	if _, err := svc.ValidateStreamToken(ctx, media, "track"); err == nil {
		t.Fatalf("stream survived logout with expired browser token (logout status=%d)", w.Code)
	}
}
