package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"discodrive/internal/auth"
	"discodrive/internal/db"
	"discodrive/internal/storage"
)

type adminHarness struct {
	h        http.Handler
	pool     *pgxpool.Pool
	q        *db.Queries
	svc      *auth.Service
	root     string
	adminTok string
	adminID  string
	userID   string
}

func newAdminHarness(t *testing.T) *adminHarness {
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
	_, user, err := svc.Register(ctx, "user@x.test", "password12")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	adm := func(h http.HandlerFunc) http.Handler { return svc.Middleware(svc.RequireAdmin(h)) }
	mux.Handle("POST /admin/users", adm(s.handleAdminCreateUser))
	mux.Handle("PATCH /admin/users/{id}", adm(s.handleAdminUpdateUser))
	mux.Handle("DELETE /admin/users/{id}", adm(s.handleAdminDeleteUser))
	return &adminHarness{h: mux, pool: pool, q: q, svc: svc, root: root, adminTok: adminTok,
		adminID: db.UUIDString(admin.ID), userID: db.UUIDString(user.ID)}
}

func (a *adminHarness) req(method, path, tok, body string) *httptest.ResponseRecorder {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, bytes.NewBufferString(body))
		r.Header.Set("Content-Type", "application/json")
	}
	r.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	a.h.ServeHTTP(rec, r)
	return rec
}

func (a *adminHarness) user(t *testing.T, id string) db.User {
	t.Helper()
	uid, _ := db.ParseUUID(id)
	u, err := a.q.GetUserByID(context.Background(), uid)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// The role is re-read on every request and the bootstrap latch never reopens: an admin
// who demotes themselves (or the last admin) leaves the server without one, fixable
// only in SQL.
func TestAdminCannotDemoteSelfOrLastAdmin(t *testing.T) {
	a := newAdminHarness(t)
	if rec := a.req("PATCH", "/admin/users/"+a.adminID, a.adminTok, `{"role":"user"}`); rec.Code != http.StatusConflict {
		t.Fatalf("self-demotion: %d %s, want 409", rec.Code, rec.Body)
	}
	if a.user(t, a.adminID).Role != "admin" {
		t.Fatal("self-demotion went through")
	}
	// A caller whose own admin role was revoked in the meantime (stale request) still
	// cannot demote the one admin left.
	uid, _ := db.ParseUUID(a.userID)
	adminID, _ := db.ParseUUID(a.adminID)
	if _, err := a.svc.AdminUpdateUser(context.Background(), uid, adminID, "user", auth.QuotaChange{}); !errors.Is(err, auth.ErrLastAdmin) {
		t.Fatalf("demoting the last admin: %v, want ErrLastAdmin", err)
	}
	if err := a.svc.AdminDeleteUser(context.Background(), uid, adminID); !errors.Is(err, auth.ErrLastAdmin) {
		t.Fatalf("deleting the last admin: %v, want ErrLastAdmin", err)
	}
	// Changing only the quota of the (sole) admin is fine.
	if rec := a.req("PATCH", "/admin/users/"+a.adminID, a.adminTok, `{"quota":1024}`); rec.Code != http.StatusOK {
		t.Fatalf("quota-only update of self: %d %s", rec.Code, rec.Body)
	}
}

// Two admins demoting each other at the same moment must not both succeed.
func TestAdminMutualDemotionKeepsAnAdmin(t *testing.T) {
	a := newAdminHarness(t)
	ctx := context.Background()
	for round := range 5 {
		tok2, second, err := a.svc.Register(ctx, "second"+string(rune('a'+round))+"@x.test", "password12")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.pool.Exec(ctx, "UPDATE users SET role = 'admin' WHERE id = $1", second.ID); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			a.req("PATCH", "/admin/users/"+db.UUIDString(second.ID), a.adminTok, `{"role":"user"}`)
		}()
		go func() { defer wg.Done(); a.req("PATCH", "/admin/users/"+a.adminID, tok2, `{"role":"user"}`) }()
		wg.Wait()
		n, err := a.q.CountAdmins(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			t.Fatalf("round %d: mutual demotion left no admin", round)
		}
		if a.user(t, a.adminID).Role != "admin" {
			return // the first admin lost; the harness token is useless now, stop here
		}
	}
}

// PATCH without "quota" used to write NULL (silently removing the quota); now absent
// = unchanged, null = remove, a number = set; negatives are refused.
func TestAdminQuotaIsTriState(t *testing.T) {
	a := newAdminHarness(t)
	path := "/admin/users/" + a.userID
	if rec := a.req("PATCH", path, a.adminTok, `{"role":"user","quota":5368709120}`); rec.Code != http.StatusOK {
		t.Fatalf("set quota: %d %s", rec.Code, rec.Body)
	}
	if rec := a.req("PATCH", path, a.adminTok, `{"role":"user"}`); rec.Code != http.StatusOK {
		t.Fatalf("role-only update: %d %s", rec.Code, rec.Body)
	}
	if q := a.user(t, a.userID).StorageQuota; !q.Valid || q.Int64 != 5368709120 {
		t.Fatalf("PATCH without quota changed it to %+v", q)
	}
	for _, bad := range []string{`{"quota":-1}`, `{"quota":"5"}`, `{"quota":1.5}`, `{"role":"root"}`} {
		if rec := a.req("PATCH", path, a.adminTok, bad); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d, want 400", bad, rec.Code)
		}
	}
	if q := a.user(t, a.userID).StorageQuota; !q.Valid || q.Int64 != 5368709120 {
		t.Fatalf("a rejected PATCH changed the quota to %+v", q)
	}
	if rec := a.req("PATCH", path, a.adminTok, `{"role":"user","quota":null}`); rec.Code != http.StatusOK {
		t.Fatalf("remove quota: %d %s", rec.Code, rec.Body)
	}
	if q := a.user(t, a.userID).StorageQuota; q.Valid {
		t.Fatalf("quota null did not remove the quota: %+v", q)
	}
	if rec := a.req("POST", "/admin/users", a.adminTok, `{"email":"neg@x.test","password":"password12","role":"user","quota":-5}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("create with negative quota: %d, want 400", rec.Code)
	}
}

// The self-delete guard compared the raw path value with the canonical id: an
// upper-case or undashed spelling of one's own id went through.
func TestAdminCannotDeleteSelfWithOtherUUIDSpelling(t *testing.T) {
	a := newAdminHarness(t)
	for _, id := range []string{strings.ToUpper(a.adminID), strings.ReplaceAll(a.adminID, "-", ""), a.adminID} {
		if rec := a.req("DELETE", "/admin/users/"+id, a.adminTok, ""); rec.Code != http.StatusBadRequest {
			t.Fatalf("DELETE own id as %q: %d, want 400", id, rec.Code)
		}
	}
	a.user(t, a.adminID) // still there
}

// Deleting a user used to leave "<uid>/", "saved/<uid>/", "podcasts/<uid>/", ".versions/<uid>/"
// and ".trash/<uid>/" on disk.
func TestAdminDeleteUserRemovesFiles(t *testing.T) {
	a := newAdminHarness(t)
	dirs := []string{a.userID, filepath.Join("saved", a.userID, "favicons"), filepath.Join("podcasts", a.userID, "covers"),
		filepath.Join(".versions", a.userID, "node"), filepath.Join(".trash", a.userID)}
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(a.root, d), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(a.root, d, "f.bin"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Another user's files stay.
	keep := filepath.Join(a.root, a.adminID, "keep.txt")
	_ = os.MkdirAll(filepath.Dir(keep), 0o755)
	_ = os.WriteFile(keep, []byte("x"), 0o644)

	if rec := a.req("DELETE", "/admin/users/"+a.userID, a.adminTok, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	deadline := time.Now().Add(5 * time.Second)
	for _, d := range append(dirs, filepath.Join(deletedUsersDir, a.userID)) {
		for {
			_, err := os.Stat(filepath.Join(a.root, d))
			if os.IsNotExist(err) {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s still on disk after the user was deleted", d)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("another user's file was touched: %v", err)
	}
}

func TestParseQuotaChange(t *testing.T) {
	for raw, want := range map[string]string{"": "unchanged", "null": "remove", "0": "set 0", "42": "set 42"} {
		c, ok := parseQuotaChange(json.RawMessage(raw))
		got := "unchanged"
		switch {
		case !ok:
			got = "invalid"
		case c.Set && c.Bytes == nil:
			got = "remove"
		case c.Set:
			got = "set " + string(json.RawMessage(raw))
		}
		if got != want {
			t.Errorf("%q: %s, want %s", raw, got, want)
		}
	}
}
