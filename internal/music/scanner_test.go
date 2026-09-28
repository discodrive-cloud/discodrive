package music

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"discodrive/internal/db"
	"discodrive/internal/music/musictest"
)

// setupDB spins up a Postgres testcontainer, runs migrations, and returns a
// query set + teardown. Skips the test if Docker is unavailable.
func setupDB(t *testing.T) (*db.Queries, context.Context) {
	t.Helper()
	ctx := context.Background()
	pgC, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("kf"), tcpostgres.WithUsername("kf"), tcpostgres.WithPassword("kf"),
		tcpostgres.BasicWaitStrategies())
	if err != nil {
		t.Skipf("Docker unavailable: %v", err)
	}
	t.Cleanup(func() { _ = pgC.Terminate(ctx) })
	dsn, _ := pgC.ConnectionString(ctx, "sslmode=disable")
	if err := db.MigrateUp(dsn); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return db.New(pool), ctx
}

// makeTenant creates a tenant + user and returns the user UUID string.
func makeTenant(t *testing.T, q *db.Queries, ctx context.Context) string {
	t.Helper()
	tenant, err := q.CreateTenant(ctx, "t")
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	u, err := q.CreateUser(ctx, db.CreateUserParams{
		TenantID:     tenant.ID,
		Email:        "scanner-test@example.com",
		PasswordHash: "x",
		Role:         "user",
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	return db.UUIDString(u.ID)
}

// requireFFmpeg skips the test when ffmpeg is not available on PATH.

// synthesizeAudio creates a short test audio file at dst using ffmpeg.
// title, artist, album are embedded as metadata tags.

// TestReadMetaMP3 synthesizes a 1-second MP3 and asserts that ReadMeta extracts
// the embedded tags.
func TestReadMetaMP3(t *testing.T) {
	requireFFmpeg(t)

	dir := t.TempDir()
	dst := filepath.Join(dir, "test.mp3")
	synthesizeAudio(t, dst, "Hello", "Artie", "Albie", "mp3")

	meta, err := ReadMeta(dst)
	if err != nil {
		t.Fatalf("ReadMeta: %v", err)
	}
	if meta.Title != "Hello" {
		t.Errorf("Title = %q, want %q", meta.Title, "Hello")
	}
	if meta.Artist != "Artie" {
		t.Errorf("Artist = %q, want %q", meta.Artist, "Artie")
	}
	if meta.Album != "Albie" {
		t.Errorf("Album = %q, want %q", meta.Album, "Albie")
	}
	if meta.Suffix != "mp3" {
		t.Errorf("Suffix = %q, want %q", meta.Suffix, "mp3")
	}
	if meta.ContentType != "audio/mpeg" {
		t.Errorf("ContentType = %q, want %q", meta.ContentType, "audio/mpeg")
	}
}

// TestReadMetaFLAC synthesizes a 1-second FLAC and asserts that ReadMeta
// extracts the embedded tags.
func TestReadMetaFLAC(t *testing.T) {
	requireFFmpeg(t)

	dir := t.TempDir()
	dst := filepath.Join(dir, "test.flac")
	synthesizeAudio(t, dst, "FLACHello", "FLACArtie", "FLACAlbie", "flac")

	meta, err := ReadMeta(dst)
	if err != nil {
		t.Fatalf("ReadMeta: %v", err)
	}
	if meta.Title != "FLACHello" {
		t.Errorf("Title = %q, want %q", meta.Title, "FLACHello")
	}
	if meta.Artist != "FLACArtie" {
		t.Errorf("Artist = %q, want %q", meta.Artist, "FLACArtie")
	}
	if meta.Suffix != "flac" {
		t.Errorf("Suffix = %q, want %q", meta.Suffix, "flac")
	}
	if meta.ContentType != "audio/flac" {
		t.Errorf("ContentType = %q, want %q", meta.ContentType, "audio/flac")
	}
}

// TestIndexNodeUpserts indexes a file node, asserts the artist/album/song rows
// are created, and verifies that calling IndexNode again does not duplicate rows.
func TestIndexNodeUpserts(t *testing.T) {
	requireFFmpeg(t)
	q, ctx := setupDB(t)
	userID := makeTenant(t, q, ctx)

	storageRoot := t.TempDir()
	ix := NewIndexer(q, storageRoot)

	// Synthesize an MP3 on disk.
	songPath := filepath.Join(storageRoot, "song.mp3")
	synthesizeAudio(t, songPath, "Upsert Song", "Upsert Artist", "Upsert Album", "mp3")

	uid, _ := db.ParseUUID(userID)

	// Create a file node pointing at the synthesized file.
	node, err := q.CreateNode(ctx, db.CreateNodeParams{
		UserID:   uid,
		Name:     "song.mp3",
		IsDir:    false,
		DiskPath: pgtype.Text{String: "song.mp3", Valid: true},
	})
	if err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	nodeID := db.UUIDString(node.ID)

	// First index call — should create artist, album, song.
	if err := ix.IndexNode(ctx, userID, nodeID, songPath); err != nil {
		t.Fatalf("IndexNode (first): %v", err)
	}

	song1, err := q.GetSongByNode(ctx, node.ID)
	if err != nil {
		t.Fatalf("GetSongByNode after first index: %v", err)
	}
	if song1.Title != "Upsert Song" {
		t.Errorf("Title = %q, want %q", song1.Title, "Upsert Song")
	}

	// Second call — must not duplicate rows.
	if err := ix.IndexNode(ctx, userID, nodeID, songPath); err != nil {
		t.Fatalf("IndexNode (second): %v", err)
	}
	song2, err := q.GetSongByNode(ctx, node.ID)
	if err != nil {
		t.Fatalf("GetSongByNode after second index: %v", err)
	}
	if song1.ID != song2.ID {
		t.Errorf("song ID changed on second upsert: %v → %v", song1.ID, song2.ID)
	}

	// Verify album song_count = 1 (not 2).
	album, err := q.UpsertAlbum(ctx, db.UpsertAlbumParams{
		UserID:   uid,
		ArtistID: song1.ArtistID,
		Name:     "Upsert Album",
	})
	if err != nil {
		t.Fatalf("re-fetch album: %v", err)
	}
	if album.SongCount != 1 {
		t.Errorf("album.SongCount = %d, want 1", album.SongCount)
	}
}

// TestResolveCoverPath checks that ResolveCoverPath finds known cover filenames.
func TestResolveCoverPath(t *testing.T) {
	dir := t.TempDir()

	// No cover yet.
	if _, ok := ResolveCoverPath(dir); ok {
		t.Fatal("expected no cover in empty dir")
	}

	// Create a cover.jpg.
	coverPath := filepath.Join(dir, "cover.jpg")
	if err := os.WriteFile(coverPath, []byte("fake"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, ok := ResolveCoverPath(dir)
	if !ok {
		t.Fatal("expected cover.jpg to be found")
	}
	if got != coverPath {
		t.Errorf("got %q, want %q", got, coverPath)
	}
}

func requireFFmpeg(t *testing.T) { musictest.RequireFFmpeg(t) }

func synthesizeAudio(t *testing.T, dst, title, artist, album, format string) {
	musictest.SynthesizeAudio(t, dst, title, artist, album, format)
}
