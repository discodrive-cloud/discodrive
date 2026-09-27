package api

import (
	"context"
	"discodrive/internal/caldav"
	"discodrive/internal/carddav"
	"discodrive/internal/db"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDAVBodyRejectsBeforeMutation(t *testing.T) {
	ctx := context.Background()
	_, q, svc := bootstrapPairingDB(t)
	token, u, err := svc.Register(ctx, "dav-body@example.test", "password12")
	if err != nil {
		t.Fatal(err)
	}
	authCtx := streamSessionContext(t, svc, token)
	_, password, err := svc.CreateWebdavPassword(authCtx, db.UUIDString(u.ID), "test")
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"caldav.enabled", "carddav.enabled"} {
		if err := q.UpsertSetting(ctx, db.UpsertSettingParams{Key: key, Value: "true"}); err != nil {
			t.Fatal(err)
		}
	}
	const limit = 1 << 20
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		b, err := io.ReadAll(r.Body)
		if err != nil || len(b) != limit {
			t.Fatalf("body truncated: %d %v", len(b), err)
		}
		w.WriteHeader(204)
	})
	for _, h := range []http.Handler{caldav.Handler(svc, q, nil, next), carddav.Handler(svc, q, nil, next)} {
		for _, method := range []string{"PUT", "PROPFIND", "REPORT", "PROPPATCH", "MKCALENDAR"} {
			for _, chunked := range []bool{false, true} {
				called = false
				r := httptest.NewRequest(method, "/collection/object", strings.NewReader(strings.Repeat("x", limit+1)))
				r.SetBasicAuth(u.Email, password)
				if chunked {
					r.ContentLength = -1
				}
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				// Nil backends deliberately panic if mutation dispatch runs before the limit.
				if w.Code != 413 || called {
					t.Fatalf("%s chunked=%v status=%d", method, chunked, w.Code)
				}
			}
		}
		r := httptest.NewRequest("PUT", "/collection/object", strings.NewReader(strings.Repeat("x", limit)))
		r.SetBasicAuth(u.Email, password)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 204 || !called {
			t.Fatalf("boundary=%d", w.Code)
		}
	}
}
