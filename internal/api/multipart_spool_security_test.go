package api_test

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"discodrive/internal/api"
	"discodrive/internal/auth"
	davpkg "discodrive/internal/dav"
	"discodrive/internal/db"
	"discodrive/internal/quota"
	"discodrive/internal/storage"
)

// Multipart parts above the in-memory threshold are spooled to TMPDIR. Handlers run on
// request clones (auth middleware, transport policy), and net/http only cleans the
// original request's form, so every handler that parses multipart must clean up
// itself — otherwise each upload, even one refused for quota, leaves a file behind.
func TestMultipartSpoolIsRemoved(t *testing.T) {
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

	spool := t.TempDir()
	t.Setenv("TMPDIR", spool)

	q := db.New(pool)
	svc := auth.NewService(pool, auth.NewTokenIssuer("secret", time.Hour), nil)
	tok, user, err := svc.Register(ctx, "u@x.test", "password12")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	files := storage.NewFileService(pool, storage.NewLocalDisk(t.TempDir()))
	files.SetQuota(quota.New(q, 0))
	h := api.NewRouter(svc, q, files, nil, t.TempDir(), nil, nil, nil, nil, nil, nil, davpkg.NewService(pool),
		false, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	leftovers := func() int {
		t.Helper()
		m, _ := filepath.Glob(filepath.Join(spool, "multipart-*"))
		return len(m)
	}
	post := func(path, field, filename string, data []byte) int {
		t.Helper()
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		fw, _ := mw.CreateFormFile(field, filename)
		_, _ = fw.Write(data)
		_ = mw.Close()
		req, _ := http.NewRequest(http.MethodPost, srv.URL+path, &body)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	big := bytes.Repeat([]byte("x"), 2<<20) // above the 1 MiB upload threshold
	if code := post("/files/upload", "file", "a.bin", big); code != http.StatusCreated {
		t.Fatalf("upload = %d, want 201", code)
	}
	if n := leftovers(); n != 0 {
		t.Fatalf("successful upload left %d spool file(s)", n)
	}

	if _, err := q.UpdateUser(ctx, db.UpdateUserParams{
		ID: user.ID, Role: "user", StorageQuota: pgtype.Int8{Int64: 1 << 20, Valid: true},
	}); err != nil {
		t.Fatalf("set quota: %v", err)
	}
	if code := post("/files/upload", "file", "b.bin", big); code != http.StatusInsufficientStorage {
		t.Fatalf("over-quota upload = %d, want 507", code)
	}
	if n := leftovers(); n != 0 {
		t.Fatalf("refused upload left %d spool file(s)", n)
	}

	card := "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:A\r\nUID:spool-1\r\nEND:VCARD\r\n"
	if code := post("/me/contacts/import", "file", "c.vcf", append([]byte(card), bytes.Repeat([]byte("\r\n"), 2<<20)...)); code != http.StatusOK {
		t.Fatalf("contacts import = %d, want 200", code)
	}
	if n := leftovers(); n != 0 {
		t.Fatalf("contacts import left %d spool file(s)", n)
	}

	// The import body is bounded: far past the 32 MiB import limit is refused, not spooled.
	huge := strings.Repeat("y", 40<<20)
	if code := post("/me/contacts/import", "file", "d.vcf", []byte(huge)); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized contacts import = %d, want 413", code)
	}
	if n := leftovers(); n != 0 {
		t.Fatalf("oversized import left %d spool file(s)", n)
	}
}
