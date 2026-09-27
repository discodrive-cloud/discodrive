package api

import (
	"context"
	"discodrive/internal/caldav"
	"discodrive/internal/carddav"
	"discodrive/internal/db"
	"discodrive/internal/webdav"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDAVAuthenticationSharedLimitsAndRevocation(t *testing.T) {
	ctx := context.Background()
	_, q, svc := bootstrapPairingDB(t)
	token, u, err := svc.Register(ctx, "dav-limits@example.test", "password12")
	if err != nil {
		t.Fatal(err)
	}
	var password string
	create := svc.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, password, err = svc.CreateWebdavPassword(r.Context(), db.UUIDString(u.ID), "sync-test")
		if err != nil {
			t.Fatal(err)
		}
	}))
	req := httptest.NewRequest("POST", "/devices/webdav", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	create.ServeHTTP(httptest.NewRecorder(), req)
	if password == "" {
		t.Fatal("could not create DAV password")
	}

	for _, key := range []string{"webdav.enabled", "caldav.enabled", "carddav.enabled"} {
		if err := q.UpsertSetting(ctx, db.UpsertSettingParams{Key: key, Value: "true"}); err != nil {
			t.Fatal(err)
		}
	}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	handlers := []http.Handler{webdav.Auth(svc, q, next), caldav.Handler(svc, q, nil, next), carddav.Handler(svc, q, nil, next)}
	request := func(h http.Handler, peer, password string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("OPTIONS", "/", nil)
		r.RemoteAddr = peer + ":1234"
		r.SetBasicAuth(u.Email, password)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	// More than the failure budget, across all three protocols, still succeeds.
	for i := 0; i < 15; i++ {
		if w := request(handlers[i%3], "203.0.113.1", password); w.Code != 204 {
			t.Fatalf("bulk sync=%d", w.Code)
		}
	}
	for i := 0; i < 10; i++ {
		if w := request(handlers[i%3], "203.0.113.2", "wrong"); w.Code != 401 {
			t.Fatalf("failure %d=%d", i, w.Code)
		}
	}
	for _, h := range handlers {
		w := request(h, "203.0.113.2", "wrong")
		if w.Code != 429 || w.Header().Get("Retry-After") == "" {
			t.Fatalf("shared limit=%d headers=%v", w.Code, w.Header())
		}
	}
	if w := request(handlers[0], "203.0.113.3", password); w.Code != 204 {
		t.Fatalf("other client blocked=%d", w.Code)
	}
	if _, err := svc.ChangePassword(ctx, db.UUIDString(u.ID), "password12", "changed-password12"); err != nil {
		t.Fatal(err)
	}
	for _, h := range handlers {
		if w := request(h, "203.0.113.3", password); w.Code != 401 {
			t.Fatalf("revoked credentials=%d", w.Code)
		}
	}
}
