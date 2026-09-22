package api

import (
	"context"
	"discodrive/internal/db"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAppleProfileDownloadIsPrivateAndExpires(t *testing.T) {
	s := &Server{}
	token, err := s.profiles.Put("user", []byte("<plist/>"))
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /apple-profile/{ticket}/DiscoDrive.mobileconfig", s.handleAppleProfileDownload)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest("GET", "/apple-profile/"+token+"/DiscoDrive.mobileconfig", nil))
	if rr.Code != 200 || rr.Header().Get("Content-Type") != "application/x-apple-aspen-config" || !strings.Contains(rr.Header().Get("Cache-Control"), "no-store") {
		t.Fatalf("unexpected response: %d, %v", rr.Code, rr.Header())
	}
	missing := httptest.NewRecorder()
	mux.ServeHTTP(missing, httptest.NewRequest("GET", "/apple-profile/not-a-ticket/DiscoDrive.mobileconfig", nil))
	if missing.Code != http.StatusGone {
		t.Fatalf("unknown ticket: %d", missing.Code)
	}
}

func TestAppleProfileAuthenticatedSetup(t *testing.T) {
	ctx := context.Background()
	_, q, svc := bootstrapPairingDB(t)
	token, user, err := svc.Register(ctx, "profile@example.test", "test-password")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{auth: svc, q: q}
	for _, key := range []string{"caldav.enabled", "carddav.enabled"} {
		if err := q.UpsertSetting(ctx, db.UpsertSettingParams{Key: key, Value: "true", UpdatedBy: user.ID}); err != nil {
			t.Fatal(err)
		}
	}
	handler := svc.Middleware(http.HandlerFunc(s.handleAppleProfile))
	request := func(body, auth string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "https://drive.example.test/me/apple-profile", strings.NewReader(body))
		if auth != "" {
			req.Header.Set("Authorization", "Bearer "+auth)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	body := `{"server_url":"https://drive.example.test","installation_id":"fixture","calendars":true,"contacts":true}`
	if rec := request(body, ""); rec.Code != 401 {
		t.Fatalf("anonymous status %d", rec.Code)
	}
	if rec := request(strings.ReplaceAll(body, "drive.example.test", "other.example.test"), token); rec.Code != 400 {
		t.Fatalf("external origin status %d", rec.Code)
	}
	rec := request(body, token)
	if rec.Code != 201 {
		t.Fatalf("prepare: %d %s", rec.Code, rec.Body.String())
	}
	var result map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	ticket := strings.Split(result["download_path"], "/")[2]
	data, ok := s.profiles.Get(ticket)
	if !ok || !strings.Contains(string(data), user.Email) || !strings.Contains(string(data), db.UUIDString(user.ID)) {
		t.Fatal("profile is not scoped to authenticated account")
	}
	if strings.Contains(string(data), "Password") || strings.Contains(string(data), token) {
		t.Fatal("credentials leaked into profile")
	}
	if err := q.UpsertSetting(ctx, db.UpsertSettingParams{Key: "carddav.enabled", Value: "false", UpdatedBy: user.ID}); err != nil {
		t.Fatal(err)
	}
	if rec := request(body, token); rec.Code != 403 {
		t.Fatalf("disabled service status %d", rec.Code)
	}
}
