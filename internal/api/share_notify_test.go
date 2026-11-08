package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"discodrive/internal/api"
	"discodrive/internal/auth"
	davpkg "discodrive/internal/dav"
	"discodrive/internal/db"
	"discodrive/internal/notify"
	"discodrive/internal/storage"
)

type fakeMail struct {
	mu   sync.Mutex
	sent []notify.Message
}

func (f *fakeMail) Name() string { return "email" }
func (f *fakeMail) Send(_ context.Context, m notify.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, m)
	return nil
}
func (f *fakeMail) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

// Sharing the same thing with the same person again changes nothing, so it must not
// mail them again: otherwise any user can send another user an unlimited stream of
// emails, with a subject the sender picks, from the operator's mail server.
func TestRepeatShareMailsOnce(t *testing.T) {
	ctx := context.Background()
	pgC, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("kf"), tcpostgres.WithUsername("kf"), tcpostgres.WithPassword("kf"),
		tcpostgres.BasicWaitStrategies())
	if err != nil {
		t.Skipf("Docker required: %v", err)
	}
	t.Cleanup(func() { _ = pgC.Terminate(ctx) })
	dsn, _ := pgC.ConnectionString(ctx, "sslmode=disable")
	if err := db.MigrateUp(dsn); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	pool, _ := pgxpool.New(ctx, dsn)
	t.Cleanup(pool.Close)
	q := db.New(pool)
	svc := auth.NewService(pool, auth.NewTokenIssuer("secret", time.Hour), nil)
	tok, _, err := svc.Register(ctx, "a@x.test", "password12")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Register(ctx, "b@x.test", "password12"); err != nil {
		t.Fatal(err)
	}
	mail := &fakeMail{}
	files := storage.NewFileService(pool, storage.NewLocalDisk(t.TempDir()))
	h := api.NewRouter(svc, q, files, nil, t.TempDir(), nil, notify.New(q, mail), nil, nil, nil, nil, davpkg.NewService(pool),
		false, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	do := func(method, path string, body any) (int, map[string]any) {
		t.Helper()
		b, _ := json.Marshal(body)
		req := httptest.NewRequest(method, path, bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	waitMails := func(want int) {
		t.Helper()
		time.Sleep(300 * time.Millisecond)
		if got := mail.count(); got != want {
			t.Fatalf("%d emails sent, want %d", got, want)
		}
	}

	code, list := do("POST", "/me/task-lists", map[string]any{"name": "Groceries"})
	if code != http.StatusCreated {
		t.Fatalf("create list: %d %v", code, list)
	}
	for i := 0; i < 3; i++ {
		if code, _ := do("POST", "/me/task-lists/"+list["id"].(string)+"/share", map[string]any{"email": "b@x.test"}); code != http.StatusOK {
			t.Fatalf("share list: %d", code)
		}
	}
	waitMails(1)

	code, folder := do("POST", "/files/folder", map[string]any{"name": "docs"})
	if code != http.StatusCreated {
		t.Fatalf("create folder: %d %v", code, folder)
	}
	for i := 0; i < 3; i++ {
		if code, _ := do("POST", "/files/"+folder["id"].(string)+"/share", map[string]any{"email": "b@x.test", "access": "read"}); code != http.StatusCreated {
			t.Fatalf("share folder: %d", code)
		}
	}
	waitMails(2)
	var rows int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM resource_shares WHERE resource_type='file_node'").Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("%d file share rows after sharing the same folder 3 times, want 1", rows)
	}

	// Removing and re-adding a share makes it new again; a per-user limit on sharing
	// keeps that loop from turning into a mail cannon.
	limited := false
	for i := 0; i < 60 && !limited; i++ {
		code, _ := do("POST", "/me/task-lists/"+list["id"].(string)+"/share", map[string]any{"email": "b@x.test"})
		limited = code == http.StatusTooManyRequests
	}
	if !limited {
		t.Fatal("60 share requests in a row were never rate limited")
	}
}
