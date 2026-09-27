package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"discodrive/internal/auth"
	"discodrive/internal/db"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestAppleEnrollmentAuthenticationAndRevocation(t *testing.T) {
	ctx := context.Background()
	_, q, svc := bootstrapPairingDB(t)
	token, user, err := svc.Register(ctx, "enrollment@example.test", "test-password")
	if err != nil {
		t.Fatal(err)
	}
	_, other, err := svc.Register(ctx, "other-enrollment@example.test", "test-password")
	if err != nil {
		t.Fatal(err)
	}
	password := "fixture-password"
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	dev, err := q.CreateWebdavDevice(ctx, db.CreateWebdavDeviceParams{UserID: user.ID, Name: "test enrollment", TokenVersion: user.TokenVersion, SecretHash: pgtype.Text{String: hash, Valid: true}})
	if err != nil {
		t.Fatal(err)
	}
	otherPassword := password
	otherDev, err := q.CreateWebdavDevice(ctx, db.CreateWebdavDeviceParams{UserID: other.ID, Name: "other enrollment", TokenVersion: other.TokenVersion, SecretHash: pgtype.Text{String: hash, Valid: true}})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"caldav.enabled", "carddav.enabled"} {
		if err = q.UpsertSetting(ctx, db.UpsertSettingParams{Key: key, Value: "true", UpdatedBy: user.ID}); err != nil {
			t.Fatal(err)
		}
	}
	s := &Server{auth: svc, q: q}
	handler := svc.Middleware(http.HandlerFunc(s.handleAppleEnrollment))
	request := func(origin, device, password, bearer string) *httptest.ResponseRecorder {
		b, _ := json.Marshal(map[string]any{"server_url": origin, "device_id": device, "password": password, "installation_id": "phone", "calendars": true, "contacts": true})
		r := httptest.NewRequest("POST", "https://drive.example.test/me/apple-enrollment", strings.NewReader(string(b)))
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, r)
		return rec
	}
	if rec := request("https://drive.example.test", db.UUIDString(dev.ID), password, ""); rec.Code != 401 {
		t.Fatalf("anonymous: %d", rec.Code)
	}
	if rec := request("https://drive.example.test:444", db.UUIDString(dev.ID), password, token); rec.Code != 400 {
		t.Fatalf("wrong port: %d", rec.Code)
	}
	if rec := request("https://drive.example.test", db.UUIDString(otherDev.ID), otherPassword, token); rec.Code != 403 {
		t.Fatalf("other user credential: %d", rec.Code)
	}
	if rec := request("https://drive.example.test", db.UUIDString(dev.ID), "wrong", token); rec.Code != 403 {
		t.Fatalf("wrong password: %d", rec.Code)
	}
	rec := request("https://drive.example.test", db.UUIDString(dev.ID), password, token)
	if rec.Code != 201 {
		t.Fatalf("prepare: %d %s", rec.Code, rec.Body.String())
	}
	var result map[string]string
	if err = json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	path := result["download_path"]
	if strings.Contains(rec.Body.String(), password) {
		t.Fatal("password leaked into response")
	}
	ticket := strings.Split(path, "/")[2]
	defer s.enrollments.Cancel(ticket)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /apple-enrollment/{ticket}/{stage}", s.handleAppleEnrollmentExchange)
	download := func() *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, httptest.NewRequest("GET", path, nil))
		return rr
	}
	if rr := download(); rr.Code != 200 || !strings.Contains(rr.Header().Get("Cache-Control"), "no-store") {
		t.Fatalf("download: %d", rr.Code)
	}
	if err = q.DeleteDevice(ctx, db.DeleteDeviceParams{ID: dev.ID, UserID: user.ID}); err != nil {
		t.Fatal(err)
	}
	if rr := download(); rr.Code != 403 {
		t.Fatalf("revoked download: %d", rr.Code)
	}
	if _, _, ok := s.enrollments.Owner(ticket); ok {
		t.Fatal("revoked enrollment retained")
	}
}

func TestAppleEnrollmentCapability(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		s := &Server{enrollmentOn: enabled}
		r := httptest.NewRecorder()
		s.handleAppleEnrollmentStatus(r, httptest.NewRequest("GET", "/me/apple-enrollment", nil))
		var got struct {
			Enabled bool `json:"enabled"`
		}
		if err := json.Unmarshal(r.Body.Bytes(), &got); err != nil || got.Enabled != enabled || r.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("incorrect enrollment capability")
		}
	}
}
