package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"discodrive/internal/db"
)

func TestOnlyAdminChangesGlobalAccess(t *testing.T) {
	ctx := context.Background()
	_, q, svc := bootstrapPairingDB(t)
	tok, user, err := svc.Register(ctx, "security@example.test", "test-password")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{auth: svc, q: q}
	h := svc.Middleware(http.HandlerFunc(s.handlePutAccess))
	for _, role := range []string{"user", "admin"} {
		if role == "admin" {
			if _, err := q.UpdateUser(ctx, db.UpdateUserParams{ID: user.ID, Role: role}); err != nil {
				t.Fatal(err)
			}
		}
		req := httptest.NewRequest("PUT", "/me/access", strings.NewReader(`{"webdav":true,"caldav":true,"carddav":true}`))
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if role == "user" {
			if rec.Code != 403 {
				t.Fatalf("user status=%d", rec.Code)
			}
			for _, key := range accessFlagKeys {
				if _, err := q.GetSetting(ctx, key); err == nil {
					t.Fatal("unauthorized setting write")
				}
			}
		} else if rec.Code != 200 {
			t.Fatalf("admin status=%d", rec.Code)
		}
	}
}
