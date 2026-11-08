package links

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/db"
	"discodrive/internal/quota"
)

// One user's links must not take the whole fetch pool: a user saving dozens of links to a
// page that stalls would otherwise leave every other user's links without previews.
func TestKickCapsLinksPerUser(t *testing.T) {
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
	defer slow.Close()
	defer close(release)

	_, q, uid := bootstrap(t)
	s := testService(q)
	for i := 0; i < maxParallel+maxWaiting; i++ {
		if _, err := s.Save(context.Background(), uid, slow.URL+"/p"+strconv.Itoa(i), ""); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(200 * time.Millisecond)
	if n := int(s.waiting.Load()) + len(s.sem); n > maxPerUser {
		t.Fatalf("one user holds %d fetch slots and places, want at most %d", n, maxPerUser)
	}
}

// The backlog goes round the users, oldest first by when the server stored each link:
// the share time comes from the client, so backdating links must not jump the queue.
func TestBacklogIsFairAndIgnoresShareTime(t *testing.T) {
	pool, q, a := bootstrap(t)
	ctx := context.Background()
	tenant, _ := q.CreateTenant(ctx, "t2")
	ub, err := q.CreateUser(ctx, db.CreateUserParams{TenantID: tenant.ID, Email: "b@x", PasswordHash: "x", Role: "user"})
	if err != nil {
		t.Fatal(err)
	}
	b := ub.ID
	add := func(u pgtype.UUID, n int, shared time.Time) {
		for i := 0; i < n; i++ {
			if _, err := q.UpsertSavedLink(ctx, db.UpsertSavedLinkParams{UserID: u, Url: "https://e.test/" + strconv.Itoa(rand.Int()), Source: "other", SharedAt: pgtype.Timestamptz{Time: shared, Valid: true}}); err != nil {
				t.Fatal(err)
			}
		}
	}
	add(b, 1, time.Now())
	add(a, 30, time.Now().Add(-29*24*time.Hour)) // backdated, stored after b's
	if _, err := pool.Exec(ctx, "UPDATE saved_links SET created_at = created_at - interval '2 minutes', saved_at = LEAST(saved_at, now() - interval '2 minutes')"); err != nil {
		t.Fatal(err)
	}
	rows, err := q.ListLinksNeedingEnrich(ctx, enrichBatch)
	if err != nil {
		t.Fatal(err)
	}
	seen := false
	for _, r := range rows {
		if r.UserID == b {
			seen = true
		}
	}
	if !seen {
		t.Fatalf("a batch of %d held only the backdated user's links", len(rows))
	}
}

// Thumbnails count toward the owner's quota, so one is not stored when it does not fit.
func TestThumbNotStoredOverQuota(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 200, 200))
	for x := 0; x < 200; x++ {
		for y := 0; y < 200; y++ {
			img.Set(x, y, color.RGBA{uint8(rand.Intn(256)), uint8(rand.Intn(256)), uint8(rand.Intn(256)), 255})
		}
	}
	var pngBytes bytes.Buffer
	_ = png.Encode(&pngBytes, img)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/n.png" {
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(pngBytes.Bytes())
			return
		}
		page(`<meta property="og:image" content="/n.png">`)(w, r)
	}))
	defer srv.Close()

	_, q, uid := bootstrap(t)
	if _, err := q.UpdateUser(context.Background(), db.UpdateUserParams{ID: uid, Role: "user", StorageQuota: pgtype.Int8{Int64: 1, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	s := testService(q)
	s.SetQuota(quota.New(q, 0))
	if _, err := s.Save(context.Background(), uid, srv.URL+"/a", ""); err != nil {
		t.Fatal(err)
	}
	s.Wait()
	used, err := q.UserStorageUsage(context.Background(), uid)
	if err != nil {
		t.Fatal(err)
	}
	if used > 1 {
		t.Fatalf("a thumbnail of %d bytes was stored over a 1-byte quota", used)
	}
}

// The inbox is bounded per user: rows are not counted in the quota (only thumbnails are),
// so without a cap one account could grow the shared database without end.
func TestInboxIsCappedPerUser(t *testing.T) {
	pool, q, uid := bootstrap(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO saved_links (user_id, url) SELECT $1, 'https://e.test/' || g FROM generate_series(1, $2::int) g`, uid, maxLinksPerUser); err != nil {
		t.Fatal(err)
	}
	s := testService(q)
	s.noFetch = true
	if _, err := s.Save(ctx, uid, "https://e.test/one-more", ""); !errors.Is(err, ErrTooMany) {
		t.Fatalf("saving past %d links: %v, want ErrTooMany", maxLinksPerUser, err)
	}
	// sharing a link already in the inbox again still brings it to the top
	if _, err := s.Save(ctx, uid, "https://e.test/1", ""); err != nil {
		t.Fatalf("re-sharing an existing link at the cap: %v", err)
	}
}
