package links

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"discodrive/internal/db"
)

// bootstrap spins up Postgres, runs migrations and creates one user.
func bootstrap(t *testing.T) (*pgxpool.Pool, *db.Queries, pgtype.UUID) {
	t.Helper()
	ctx := context.Background()
	pgC, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("kf"), tcpostgres.WithUsername("kf"), tcpostgres.WithPassword("kf"),
		tcpostgres.BasicWaitStrategies())
	if err != nil {
		t.Skipf("need Docker: %v", err)
	}
	t.Cleanup(func() { _ = pgC.Terminate(ctx) })
	dsn, _ := pgC.ConnectionString(ctx, "sslmode=disable")
	if err := db.MigrateUp(dsn); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool: %v", err)
	}
	t.Cleanup(pool.Close)
	q := db.New(pool)
	tenant, _ := q.CreateTenant(ctx, "t")
	u, err := q.CreateUser(ctx, db.CreateUserParams{TenantID: tenant.ID, Email: "u@x", PasswordHash: "x", Role: "user"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	return pool, q, u.ID
}

func TestUpsertDedupAndUnarchive(t *testing.T) {
	pool, q, uid := bootstrap(t)
	ctx := context.Background()
	a, err := q.UpsertSavedLink(ctx, db.UpsertSavedLinkParams{UserID: uid, Url: "https://www.instagram.com/reel/A/", Source: "instagram", Title: "Баклажаны", SharedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.SetSavedLinkArchived(ctx, db.SetSavedLinkArchivedParams{ID: a.ID, UserID: uid, Archived: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "UPDATE saved_links SET saved_at = saved_at - interval '1 day' WHERE id = $1", a.ID); err != nil {
		t.Fatal(err)
	}
	b, err := q.UpsertSavedLink(ctx, db.UpsertSavedLinkParams{UserID: uid, Url: "https://www.instagram.com/reel/A/", Source: "instagram", Title: "", SharedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true}})
	if err != nil {
		t.Fatal(err)
	}
	if b.ID != a.ID {
		t.Fatal("re-share must not create a duplicate")
	}
	if b.ArchivedAt.Valid {
		t.Fatal("re-share must bring the link back to the inbox")
	}
	if !b.SavedAt.Time.After(a.SavedAt.Time) {
		t.Fatal("re-share must move the link to the top")
	}
	if b.Title != "Баклажаны" {
		t.Fatalf("empty title must not overwrite the old one, got %q", b.Title)
	}
	if n, _ := q.CountInboxLinks(ctx, uid); n != 1 {
		t.Fatalf("inbox count = %d, want 1", n)
	}
}

func TestThumbCountsInQuota(t *testing.T) {
	_, q, uid := bootstrap(t)
	ctx := context.Background()
	before, err := q.UserStorageUsage(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	l, _ := q.UpsertSavedLink(ctx, db.UpsertSavedLinkParams{UserID: uid, Url: "https://example.org/", Source: "other", SharedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true}})
	if err := q.SetSavedLinkEnriched(ctx, db.SetSavedLinkEnrichedParams{ID: l.ID, Thumb: make([]byte, 1234)}); err != nil {
		t.Fatal(err)
	}
	after, _ := q.UserStorageUsage(ctx, uid)
	if after-before != 1234 {
		t.Fatalf("usage grew by %d, want 1234", after-before)
	}
	total, _ := q.TotalStorageUsage(ctx)
	if total < 1234 {
		t.Fatalf("total usage %d does not include the thumbnail", total)
	}
}

// A queued copy of a share that already landed must not undo what the user did since.
func TestUpsertOlderShareKeepsArchive(t *testing.T) {
	pool, q, uid := bootstrap(t)
	ctx := context.Background()
	s := NewService(q)
	s.noFetch = true
	s.Validate = func(string) error { return nil }
	l, _ := s.Save(ctx, uid, "https://www.instagram.com/reel/A/", "")
	if err := s.SetArchived(ctx, uid, l.ID, true); err != nil {
		t.Fatal(err)
	}
	sharedBefore := time.Now().Add(-time.Hour)
	again, err := s.SaveShared(ctx, uid, "https://www.instagram.com/reel/A/", "", sharedBefore)
	if err != nil {
		t.Fatal(err)
	}
	if !again.Archived || !again.SavedAt.Equal(l.SavedAt) {
		t.Fatalf("an older share changed the row: %+v (was %+v)", again, l)
	}
	// A share made after the archiving brings it back.
	if again, _ = s.SaveShared(ctx, uid, "https://www.instagram.com/reel/A/", "", time.Now()); again.Archived {
		t.Fatal("a newer share must bring the link back to the inbox")
	}
	_ = pool
}

// A link whose first fetch got nothing is fetched again when it is shared again.
func TestReshareRetriesAnEmptyPreview(t *testing.T) {
	_, q, uid := bootstrap(t)
	ctx := context.Background()
	s := NewService(q)
	s.noFetch = true
	s.Validate = func(string) error { return nil }
	l, _ := s.Save(ctx, uid, "https://www.instagram.com/reel/B/", "")
	if err := q.SetSavedLinkEnriched(ctx, db.SetSavedLinkEnrichedParams{ID: l.id}); err != nil {
		t.Fatal(err)
	}
	if again, _ := s.Save(ctx, uid, "https://www.instagram.com/reel/B/", ""); again.enriched {
		t.Fatal("an empty row must be fetched again on re-share")
	}
	if err := q.SetSavedLinkEnriched(ctx, db.SetSavedLinkEnrichedParams{ID: l.id, Title: "Found"}); err != nil {
		t.Fatal(err)
	}
	if again, _ := s.Save(ctx, uid, "https://www.instagram.com/reel/B/", ""); !again.enriched {
		t.Fatal("a row with metadata must not be fetched again")
	}
}

func TestDeletingUserRemovesLinks(t *testing.T) {
	pool, q, uid := bootstrap(t)
	ctx := context.Background()
	l, _ := q.UpsertSavedLink(ctx, db.UpsertSavedLinkParams{UserID: uid, Url: "https://example.org/", Source: "other", SharedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true}})
	_ = q.SetSavedLinkEnriched(ctx, db.SetSavedLinkEnrichedParams{ID: l.ID, Thumb: []byte{1}})
	if _, err := pool.Exec(ctx, "DELETE FROM users WHERE id = $1", uid); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = pool.QueryRow(ctx, "SELECT count(*) FROM saved_links").Scan(&n)
	if n != 0 {
		t.Fatalf("%d links survived their user", n)
	}
}
