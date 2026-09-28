package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"discodrive/internal/db"
	"discodrive/internal/storage"
)

func rescanAPI(t *testing.T) (h http.Handler, adminTok, userTok, userID string) {
	t.Helper()
	pool, q, svc := bootstrapPairingDB(t)
	root := t.TempDir()
	s := &Server{auth: svc, q: q, files: storage.NewFileService(pool, storage.NewLocalDisk(root)), storageRoot: root}
	ctx := context.Background()
	adminTok, admin, err := svc.Register(ctx, "admin@x.test", "password12")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "UPDATE users SET role = 'admin' WHERE id = $1", admin.ID); err != nil {
		t.Fatal(err)
	}
	userTok, user, err := svc.Register(ctx, "user@x.test", "password12")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	adm := func(h http.HandlerFunc) http.Handler { return svc.Middleware(svc.RequireAdmin(h)) }
	mux.Handle("POST /admin/rescan", adm(s.handleAdminRescan))
	mux.Handle("GET /admin/rescan", adm(s.handleAdminRescanList))
	return mux, adminTok, userTok, db.UUIDString(user.ID)
}

func getJSON(h http.Handler, path, tok string) (*httptest.ResponseRecorder, map[string]any) {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

func TestAdminRescanQueuesAndLists(t *testing.T) {
	h, adminTok, _, userID := rescanAPI(t)
	if rec, _ := doPost(h, "/admin/rescan", adminTok, map[string]any{"user_id": userID}); rec.Code != http.StatusCreated {
		t.Fatalf("queue for user: %d %s", rec.Code, rec.Body)
	}
	if rec, _ := doPost(h, "/admin/rescan", adminTok, map[string]any{}); rec.Code != http.StatusCreated {
		t.Fatalf("queue for everyone: %d %s", rec.Code, rec.Body)
	}
	rec, out := getJSON(h, "/admin/rescan", adminTok)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d", rec.Code)
	}
	reqs := out["requests"].([]any)
	if len(reqs) != 2 {
		t.Fatalf("requests %v", reqs)
	}
	newest, oldest := reqs[0].(map[string]any), reqs[1].(map[string]any)
	if newest["user_email"] != nil || oldest["user_email"] != "user@x.test" {
		t.Fatalf("targets %v / %v", newest["user_email"], oldest["user_email"])
	}
}

func TestAdminRescanRejects(t *testing.T) {
	h, adminTok, userTok, _ := rescanAPI(t)
	if rec, _ := doPost(h, "/admin/rescan", userTok, map[string]any{}); rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin POST: %d", rec.Code)
	}
	if rec, _ := getJSON(h, "/admin/rescan", userTok); rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin GET: %d", rec.Code)
	}
	if rec, _ := doPost(h, "/admin/rescan", adminTok, map[string]any{"user_id": "00000000-0000-0000-0000-000000000001"}); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown user: %d", rec.Code)
	}
	if rec, _ := doPost(h, "/admin/rescan", adminTok, map[string]any{"user_id": "nope"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid user_id: %d", rec.Code)
	}
}
