package music

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"discodrive/internal/db"
	"discodrive/internal/music/tagwrite"
	"discodrive/internal/storage"
)

// sp returns a pointer to s — helper for building tagwrite.Tags in tests.
func sp(s string) *string { return &s }

// setupTagEditorEnv spins up a Postgres testcontainer, applies migrations, and
// returns a FileService, query set, user ID string, and a storage root path.
// Skips the test if Docker is unavailable.
func setupTagEditorEnv(t *testing.T) (*storage.FileService, *db.Queries, string, string) {
	t.Helper()
	ctx := context.Background()

	pgC, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("kf"),
		tcpostgres.WithUsername("kf"),
		tcpostgres.WithPassword("kf"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Skipf("Docker unavailable: %v", err)
	}
	t.Cleanup(func() { _ = pgC.Terminate(ctx) })

	dsn, err := pgC.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("dsn: %v", err)
	}
	if err := db.MigrateUp(dsn); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)

	q := db.New(pool)
	tenant, err := q.CreateTenant(ctx, "t")
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	user, err := q.CreateUser(ctx, db.CreateUserParams{
		TenantID:     tenant.ID,
		Email:        "tagedit-test@example.com",
		PasswordHash: "x",
		Role:         "user",
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	root := t.TempDir()
	fs := storage.NewFileService(pool, storage.NewLocalDisk(root))
	return fs, q, db.UUIDString(user.ID), root
}

// TestTagEditor_WriteReindexesSong is an integration test that:
//  1. Sets up a user with a music folder configured in music_settings.
//  2. Creates an MP3 file node inside that folder.
//  3. Calls Write to change the title (versioning on).
//  4. Asserts Read returns the new title.
//  5. Asserts the song row in the DB reflects the updated title.
func TestTagEditor_WriteReindexesSong(t *testing.T) {
	requireFFmpeg(t)
	ctx := context.Background()

	fs, q, userID, root := setupTagEditorEnv(t)
	uid, _ := db.ParseUUID(userID)

	// 1. Create the music folder directory on disk and a DB node for it.
	st := storage.NewLocalDisk(root)
	if err := st.Mkdir(userID + "/music"); err != nil {
		t.Fatalf("mkdir music: %v", err)
	}

	folderNode, err := q.CreateNode(ctx, db.CreateNodeParams{
		UserID:   uid,
		Name:     "music",
		IsDir:    true,
		DiskPath: pgtype.Text{String: userID + "/music", Valid: true},
	})
	if err != nil {
		t.Fatalf("CreateNode (folder): %v", err)
	}

	// 2. Configure music settings with versioning enabled.
	_, err = q.UpsertMusicSettings(ctx, db.UpsertMusicSettingsParams{
		UserID:            uid,
		Enabled:           true,
		FolderNodeID:      folderNode.ID,
		TagEditVersioning: true,
	})
	if err != nil {
		t.Fatalf("UpsertMusicSettings: %v", err)
	}

	// 3. Synthesize an MP3 directly on disk inside the music folder.
	musicDir := filepath.Join(root, userID, "music")
	songDisk := filepath.Join(musicDir, "track.mp3")
	synthesizeAudio(t, songDisk, "Original Title", "Artist", "Album", "mp3")

	// Create the corresponding file node.
	songNode, err := q.CreateNode(ctx, db.CreateNodeParams{
		UserID:   uid,
		ParentID: folderNode.ID,
		Name:     "track.mp3",
		IsDir:    false,
		DiskPath: pgtype.Text{String: userID + "/music/track.mp3", Valid: true},
	})
	if err != nil {
		t.Fatalf("CreateNode (song): %v", err)
	}
	nodeID := db.UUIDString(songNode.ID)

	// 4. Index the song initially so GetSongByNode can find it.
	ix := NewIndexer(q, root)
	if err := ix.IndexNode(ctx, userID, nodeID, songDisk); err != nil {
		t.Fatalf("initial IndexNode: %v", err)
	}

	// 5. Write the new title via TagEditor.
	ed := NewTagEditor(q, fs, root)
	// The working copy is made on the data disk, not in the system temp directory: with
	// that one unusable the edit still goes through.
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))
	err = ed.Write(ctx, userID, nodeID,
		tagwrite.Tags{Title: sp("Edited")},
		tagwrite.CoverKeep,
		nil,
	)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	// 6. Assert Read returns the updated title.
	info, err := ed.Read(ctx, userID, nodeID)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if info.Tags.Title == nil || *info.Tags.Title != "Edited" {
		t.Errorf("Read title = %v, want \"Edited\"", info.Tags.Title)
	}

	// 7. Assert the song row was re-indexed with the new title.
	song, err := q.GetSongByNode(ctx, songNode.ID)
	if err != nil {
		t.Fatalf("GetSongByNode: %v", err)
	}
	if song.Title != "Edited" {
		t.Errorf("indexed title = %q, want \"Edited\"", song.Title)
	}
}

// TestTagEditor_WriteFolderBulk verifies that WriteFolder applies tags to every
// audio file recursively under a subfolder and correctly reports Affected/Updated.
func TestTagEditor_WriteFolderBulk(t *testing.T) {
	requireFFmpeg(t)
	ctx := context.Background()

	fs, q, userID, root := setupTagEditorEnv(t)
	uid, _ := db.ParseUUID(userID)

	st := storage.NewLocalDisk(root)

	// Create the top-level music folder.
	if err := st.Mkdir(userID + "/music"); err != nil {
		t.Fatalf("mkdir music: %v", err)
	}
	musicFolderNode, err := q.CreateNode(ctx, db.CreateNodeParams{
		UserID:   uid,
		Name:     "music",
		IsDir:    true,
		DiskPath: pgtype.Text{String: userID + "/music", Valid: true},
	})
	if err != nil {
		t.Fatalf("CreateNode (music): %v", err)
	}

	// Configure music settings.
	_, err = q.UpsertMusicSettings(ctx, db.UpsertMusicSettingsParams{
		UserID:            uid,
		Enabled:           true,
		FolderNodeID:      musicFolderNode.ID,
		TagEditVersioning: false,
	})
	if err != nil {
		t.Fatalf("UpsertMusicSettings: %v", err)
	}

	// Create a subfolder "a" inside the music folder (proves recursion).
	if err := st.Mkdir(userID + "/music/a"); err != nil {
		t.Fatalf("mkdir music/a: %v", err)
	}
	subFolderNode, err := q.CreateNode(ctx, db.CreateNodeParams{
		UserID:   uid,
		ParentID: musicFolderNode.ID,
		Name:     "a",
		IsDir:    true,
		DiskPath: pgtype.Text{String: userID + "/music/a", Valid: true},
	})
	if err != nil {
		t.Fatalf("CreateNode (subfolder a): %v", err)
	}

	// Synthesize two MP3s inside the subfolder.
	subDir := filepath.Join(root, userID, "music", "a")
	for _, name := range []string{"x.mp3", "y.mp3"} {
		diskPath := filepath.Join(subDir, name)
		synthesizeAudio(t, diskPath, name, "Artist", "OldAlbum", "mp3")
		songNode, err2 := q.CreateNode(ctx, db.CreateNodeParams{
			UserID:   uid,
			ParentID: subFolderNode.ID,
			Name:     name,
			IsDir:    false,
			DiskPath: pgtype.Text{String: userID + "/music/a/" + name, Valid: true},
		})
		if err2 != nil {
			t.Fatalf("CreateNode (%s): %v", name, err2)
		}
		ix := NewIndexer(q, root)
		if err2 = ix.IndexNode(ctx, userID, db.UUIDString(songNode.ID), diskPath); err2 != nil {
			t.Fatalf("IndexNode (%s): %v", name, err2)
		}
	}

	ed := NewTagEditor(q, fs, root)
	folderID := db.UUIDString(subFolderNode.ID)
	res, err := ed.WriteFolder(ctx, userID, folderID,
		tagwrite.Tags{Album: sp("Compilation")}, tagwrite.CoverKeep, nil)
	if err != nil {
		t.Fatalf("WriteFolder: %v", err)
	}
	if res.Updated != 2 || res.Affected != 2 || len(res.Failed) != 0 {
		t.Fatalf("res = %+v, want Updated=2 Affected=2 Failed=[]", res)
	}

	// Verify both songs were re-indexed with the new album.
	fileNodes, err := q.ListFileNodesUnderFolder(ctx, subFolderNode.ID)
	if err != nil {
		t.Fatalf("ListFileNodesUnderFolder: %v", err)
	}
	for _, n := range fileNodes {
		s, err := q.GetSongByNode(ctx, n.ID)
		if err != nil {
			t.Fatalf("GetSongByNode(%s): %v", n.Name, err)
		}
		album, err := q.GetAlbumWithArtist(ctx, s.AlbumID)
		if err != nil {
			t.Fatalf("GetAlbumWithArtist(%s): %v", n.Name, err)
		}
		if album.Name != "Compilation" {
			t.Errorf("song %s album = %q, want \"Compilation\"", n.Name, album.Name)
		}
	}
}

// TestSanitizeBulkTags reproduces the live data-loss bug: a bulk request that
// carries empty values for unchecked fields (Album/Genre/etc.) plus one real
// value (Artist) must NOT clear the empties — only the real value is applied,
// and per-song Title/Track are never touched.
func TestSanitizeBulkTags(t *testing.T) {
	empty := ""
	zero := 0
	nine := 9
	artist := "Би-2"
	in := tagwrite.Tags{
		Title:       sp("should be dropped"),
		Track:       &nine,
		Artist:      &artist,
		Album:       &empty, // unchecked → empty → must become nil (untouched)
		AlbumArtist: &empty,
		Genre:       &empty,
		Year:        &zero,
		Disc:        &zero,
	}
	got := sanitizeBulkTags(in)
	if got.Title != nil {
		t.Errorf("Title must be nil in bulk, got %v", *got.Title)
	}
	if got.Track != nil {
		t.Errorf("Track must be nil in bulk, got %v", *got.Track)
	}
	if got.Artist == nil || *got.Artist != "Би-2" {
		t.Errorf("Artist must be applied, got %v", got.Artist)
	}
	for name, p := range map[string]*string{"Album": got.Album, "AlbumArtist": got.AlbumArtist, "Genre": got.Genre} {
		if p != nil {
			t.Errorf("%s must be nil (untouched), got %q — empty value would WIPE it", name, *p)
		}
	}
	if got.Year != nil {
		t.Errorf("Year must be nil (untouched), got %d", *got.Year)
	}
	if got.Disc != nil {
		t.Errorf("Disc must be nil (untouched), got %d", *got.Disc)
	}
}
