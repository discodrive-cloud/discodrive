package db_test

import (
	"context"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"discodrive/internal/db"
)

// photosTestDB starts Postgres, migrates it and returns queries plus a user.
func photosTestDB(t *testing.T) (context.Context, *db.Queries, db.User, *pgxpool.Pool) {
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
	// Every down migration (photos included) applies cleanly and up works again.
	if err := db.MigrateDown(dsn); err != nil {
		t.Fatalf("migrate down: %v", err)
	}
	if err := db.MigrateUp(dsn); err != nil {
		t.Fatalf("migrate up again: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool: %v", err)
	}
	t.Cleanup(pool.Close)

	q := db.New(pool)
	tenant, err := q.CreateTenant(ctx, "test-tenant")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	u, err := q.CreateUser(ctx, db.CreateUserParams{
		TenantID: tenant.ID, Email: "photos@test.local", PasswordHash: "x", Role: "user",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	return ctx, q, u, pool
}

func TestPhotosMigration_UpDown(t *testing.T) {
	ctx, q, u, pool := photosTestDB(t)

	if _, err := q.GetPhotoSettings(ctx, u.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expected pgx.ErrNoRows for absent photo_settings row, got: %v", err)
	}
	ps, err := q.UpsertPhotoSettings(ctx, db.UpsertPhotoSettingsParams{UserID: u.ID, Enabled: true})
	if err != nil {
		t.Fatalf("UpsertPhotoSettings: %v", err)
	}
	if !ps.Enabled || ps.IndexedSeq != 0 {
		t.Fatalf("unexpected settings after upsert: %+v", ps)
	}
	if err := q.SetPhotoIndexedSeq(ctx, db.SetPhotoIndexedSeqParams{UserID: u.ID, IndexedSeq: 10}); err != nil {
		t.Fatal(err)
	}
	if err := q.SetPhotoIndexedSeq(ctx, db.SetPhotoIndexedSeqParams{UserID: u.ID, IndexedSeq: 5}); err != nil {
		t.Fatal(err)
	}
	got, _ := q.GetPhotoSettings(ctx, u.ID)
	if got.IndexedSeq != 10 {
		t.Fatalf("cursor moved backwards: %d", got.IndexedSeq)
	}

	// The tables exist after the down/up round trip.
	for _, tbl := range []string{"photo_settings", "photo_sources", "photo_assets"} {
		var ok bool
		if err := pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, tbl).Scan(&ok); err != nil || !ok {
			t.Fatalf("table %s missing: %v", tbl, err)
		}
	}
}

func TestPhotoQueries_BucketsDedupAndFilters(t *testing.T) {
	ctx, q, u, _ := photosTestDB(t)

	mkNode := func(name, path, hash string) pgtype.UUID {
		n, err := q.CreateNode(ctx, db.CreateNodeParams{
			UserID: u.ID, Name: name, DiskPath: pgtype.Text{String: path, Valid: true},
			ContentHash: pgtype.Text{String: hash, Valid: hash != ""},
		})
		if err != nil {
			t.Fatalf("CreateNode %s: %v", name, err)
		}
		return n.ID
	}
	mkAsset := func(node pgtype.UUID, kind string, when time.Time, hash string) db.PhotoAsset {
		a, err := q.UpsertPhotoAsset(ctx, db.UpsertPhotoAssetParams{
			UserID: u.ID, NodeID: node, Kind: kind, DateSource: "exif",
			TakenAt:     pgtype.Timestamptz{Time: when, Valid: true},
			TakenLocal:  pgtype.Timestamp{Time: when, Valid: true},
			ContentHash: pgtype.Text{String: hash, Valid: hash != ""},
		})
		if err != nil {
			t.Fatalf("UpsertPhotoAsset: %v", err)
		}
		return a
	}
	jan := time.Date(2024, 1, 10, 12, 0, 0, 0, time.UTC)
	feb := time.Date(2024, 2, 10, 12, 0, 0, 0, time.UTC)

	// Two copies of one picture (same hash), one RAW, one video, one in another folder.
	a1 := mkAsset(mkNode("a.jpg", "u/cam/a.jpg", "h1"), "image", jan, "h1")
	a2 := mkAsset(mkNode("a-copy.jpg", "u/cam/a-copy.jpg", "h1"), "image", jan, "h1")
	raw := mkAsset(mkNode("b.dng", "u/cam/b.dng", "h2"), "image", jan, "h2")
	vid := mkAsset(mkNode("c.mov", "u/cam/c.mov", "h3"), "video", feb, "h3")
	other := mkAsset(mkNode("d.jpg", "u/other/d.jpg", ""), "image", feb, "")
	other2 := mkAsset(mkNode("e.jpg", "u/other/e.jpg", ""), "image", feb, "") // NULL hashes never dedup
	_ = other2

	// Live photo: a child video under `other` is not a feed row but marks `other` as live.
	liveVid := mkAsset(mkNode("d.mov", "u/other/d.mov", "h4"), "video", feb, "h4")
	if err := q.SetPhotoParent(ctx, db.SetPhotoParentParams{
		ID: liveVid.ID, ParentID: other.ID, Role: pgtype.Text{String: "live-video", Valid: true},
	}); err != nil {
		t.Fatal(err)
	}

	count := func(p db.ListPhotoMonthBucketsParams) map[string]int64 {
		p.UserID = u.ID
		rows, err := q.ListPhotoMonthBuckets(ctx, p)
		if err != nil {
			t.Fatalf("ListPhotoMonthBuckets: %v", err)
		}
		m := map[string]int64{}
		for _, r := range rows {
			m[r.Month.Time.Format("2006-01")] = r.Count
		}
		return m
	}
	eq := func(name string, got, want map[string]int64) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s: got %v want %v", name, got, want)
		}
		for k, v := range want {
			if got[k] != v {
				t.Fatalf("%s: got %v want %v", name, got, want)
			}
		}
	}

	// Jan: a1 (a2 deduped) + raw = 2. Feb: vid + other + other2 = 3 (the live child is excluded).
	eq("all", count(db.ListPhotoMonthBucketsParams{}), map[string]int64{"2024-01": 2, "2024-02": 3})
	eq("video", count(db.ListPhotoMonthBucketsParams{Kind: pgtype.Text{String: "video", Valid: true}}),
		map[string]int64{"2024-02": 1})
	eq("raw", count(db.ListPhotoMonthBucketsParams{Raw: pgtype.Bool{Bool: true, Valid: true}}),
		map[string]int64{"2024-01": 1})
	eq("live", count(db.ListPhotoMonthBucketsParams{Live: pgtype.Bool{Bool: true, Valid: true}}),
		map[string]int64{"2024-02": 1})

	// Source filter: only the "cam" folder.
	camDir, err := q.CreateNode(ctx, db.CreateNodeParams{
		UserID: u.ID, Name: "cam", IsDir: true, DiskPath: pgtype.Text{String: "u/cam", Valid: true}})
	if err != nil {
		t.Fatal(err)
	}
	src, err := q.InsertPhotoSource(ctx, db.InsertPhotoSourceParams{UserID: u.ID, FolderNodeID: camDir.ID})
	if err != nil {
		t.Fatal(err)
	}
	eq("source", count(db.ListPhotoMonthBucketsParams{SourceID: src.ID}),
		map[string]int64{"2024-01": 2, "2024-02": 1})

	// Favorite, then hide and trash drop rows from the feed.
	if err := q.SetPhotoFlags(ctx, db.SetPhotoFlagsParams{
		UserID: u.ID, Ids: []pgtype.UUID{raw.ID}, Favorite: pgtype.Bool{Bool: true, Valid: true},
	}); err != nil {
		t.Fatal(err)
	}
	eq("favorite", count(db.ListPhotoMonthBucketsParams{Favorite: pgtype.Bool{Bool: true, Valid: true}}),
		map[string]int64{"2024-01": 1})
	// Dedup works among rows that pass the filter: favoriting only the higher-id copy of
	// the duplicate pair must show it under favorite=true although its lower-id twin fails.
	hi := a1.ID
	if db.UUIDString(a2.ID) > db.UUIDString(a1.ID) {
		hi = a2.ID
	}
	if err := q.SetPhotoFlags(ctx, db.SetPhotoFlagsParams{
		UserID: u.ID, Ids: []pgtype.UUID{hi}, Favorite: pgtype.Bool{Bool: true, Valid: true},
	}); err != nil {
		t.Fatal(err)
	}
	eq("favorite twin", count(db.ListPhotoMonthBucketsParams{Favorite: pgtype.Bool{Bool: true, Valid: true}}),
		map[string]int64{"2024-01": 2})
	favRows, err := q.ListPhotoAssetsInMonth(ctx, db.ListPhotoAssetsInMonthParams{
		UserID: u.ID, Month: pgtype.Timestamp{Time: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC), Valid: true},
		Favorite: pgtype.Bool{Bool: true, Valid: true},
	})
	if err != nil || len(favRows) != 2 {
		t.Fatalf("favorite month rows: %d %v", len(favRows), err)
	}
	if err := q.SetPhotoFlags(ctx, db.SetPhotoFlagsParams{
		UserID: u.ID, Ids: []pgtype.UUID{raw.ID}, Hidden: pgtype.Bool{Bool: true, Valid: true},
	}); err != nil {
		t.Fatal(err)
	}
	if err := q.TrashPhotoAssetByNode(ctx, vid.NodeID); err != nil {
		t.Fatal(err)
	}
	eq("hidden+trashed", count(db.ListPhotoMonthBucketsParams{}), map[string]int64{"2024-01": 1, "2024-02": 2})

	// The month list agrees with the bucket: the smallest id of the duplicate pair wins.
	rows, err := q.ListPhotoAssetsInMonth(ctx, db.ListPhotoAssetsInMonthParams{
		UserID: u.ID, Month: pgtype.Timestamp{Time: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC), Valid: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("january rows: %d", len(rows))
	}
	want := a1.ID
	if db.UUIDString(a2.ID) < db.UUIDString(a1.ID) {
		want = a2.ID
	}
	if rows[0].ID != want {
		t.Fatalf("dedup kept %s, want smallest id %s", db.UUIDString(rows[0].ID), db.UUIDString(want))
	}

	hid, _ := q.ListHiddenPhotoAssets(ctx, db.ListHiddenPhotoAssetsParams{
		UserID: u.ID, Month: pgtype.Timestamp{Time: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC), Valid: true}})
	tr, _ := q.ListTrashedPhotoAssets(ctx, u.ID)
	if len(hid) != 1 || len(tr) != 1 {
		t.Fatalf("hidden=%d trashed=%d", len(hid), len(tr))
	}
}

// TestPhotoQueries_Bench seeds ~90k assets and prints EXPLAIN ANALYZE timings of the feed
// queries. Run with PHOTOS_BENCH=1.
func TestPhotoQueries_Bench(t *testing.T) {
	if os.Getenv("PHOTOS_BENCH") == "" {
		t.Skip("set PHOTOS_BENCH=1")
	}
	ctx, q, u, pool := photosTestDB(t)
	_ = q
	uid := db.UUIDString(u.ID)
	// 90k nodes+assets over ~15 years; every 20th row shares a hash with its predecessor.
	if _, err := pool.Exec(ctx, `
INSERT INTO nodes (id, user_id, name, disk_path, content_hash)
SELECT gen_random_uuid(), $1::uuid, 'f' || g || CASE WHEN g % 50 = 0 THEN '.dng' ELSE '.jpg' END,
       'u/cam/' || (g % 100) || '/f' || g, 'h' || (g - (g % 20 = 1)::int)
FROM generate_series(1, 90000) g`, uid); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO photo_assets (user_id, node_id, kind, taken_at, taken_local, content_hash, favorite)
SELECT user_id, id, 'image',
       timestamptz '2010-01-01' + (row_number() OVER ())::int * interval '90 minutes',
       timestamp '2010-01-01' + (row_number() OVER ())::int * interval '90 minutes',
       content_hash, random() < 0.05
FROM nodes WHERE user_id = $1::uuid`, uid); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `ANALYZE`); err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile("photos.sql.go")
	if err != nil {
		t.Fatal(err)
	}
	constSQL := func(name string) string {
		m := regexp.MustCompile("(?s)const " + name + " = `(.*?)`").FindSubmatch(src)
		if m == nil {
			t.Fatalf("const %s not found", name)
		}
		return string(m[1])
	}
	explain := func(name, sql string, args ...any) {
		for run := 0; run < 2; run++ {
			rows, err := pool.Query(ctx, "EXPLAIN (ANALYZE, COSTS OFF) "+sql, args...)
			if err != nil {
				t.Fatal(err)
			}
			for rows.Next() {
				var line string
				_ = rows.Scan(&line)
				if run == 1 && (strings.Contains(line, "Execution Time") || strings.Contains(line, "Planning Time")) {
					t.Logf("%s: %s", name, strings.TrimSpace(line))
				}
			}
			rows.Close()
		}
	}
	var none any
	month := time.Date(2015, 3, 1, 0, 0, 0, 0, time.UTC)
	explain("buckets/no filter", constSQL("listPhotoMonthBuckets"), u.ID, none, none, none, none, none)
	explain("buckets/favorite", constSQL("listPhotoMonthBuckets"), u.ID, none, none, none, true, none)
	explain("month/no filter", constSQL("listPhotoAssetsInMonth"), u.ID, month, none, none, none, none, none)
}
