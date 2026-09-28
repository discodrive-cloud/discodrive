package music

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/db"
)

// TestScanFolderIndexesSubtree creates a directory node with two audio-file
// children, calls ScanFolder, and asserts that both files are indexed under
// their correct artists/albums.
func TestScanFolderIndexesSubtree(t *testing.T) {
	requireFFmpeg(t)
	q, ctx := setupDB(t)
	userID := makeTenant(t, q, ctx)

	storageRoot := t.TempDir()
	ix := NewIndexer(q, storageRoot)

	uid, _ := db.ParseUUID(userID)

	// Create the music subdirectory on disk.
	musicDir := filepath.Join(storageRoot, "music")
	if err := os.MkdirAll(musicDir, 0o755); err != nil {
		t.Fatalf("mkdir music: %v", err)
	}

	// Create a directory node that acts as the music folder root.
	folderNode, err := q.CreateNode(ctx, db.CreateNodeParams{
		UserID:   uid,
		Name:     "music",
		IsDir:    true,
		DiskPath: pgtype.Text{String: "music", Valid: true},
	})
	if err != nil {
		t.Fatalf("CreateNode (folder): %v", err)
	}

	// Synthesize two MP3 files on disk under storageRoot/music/.
	songA := filepath.Join(storageRoot, "music", "songA.mp3")
	songB := filepath.Join(storageRoot, "music", "songB.mp3")
	synthesizeAudio(t, songA, "Song A", "Artist A", "Album A", "mp3")
	synthesizeAudio(t, songB, "Song B", "Artist B", "Album B", "mp3")

	// Create two file nodes pointing to those files.
	nodeA, err := q.CreateNode(ctx, db.CreateNodeParams{
		UserID:   uid,
		ParentID: folderNode.ID,
		Name:     "songA.mp3",
		IsDir:    false,
		DiskPath: pgtype.Text{String: "music/songA.mp3", Valid: true},
	})
	if err != nil {
		t.Fatalf("CreateNode (songA): %v", err)
	}
	nodeB, err := q.CreateNode(ctx, db.CreateNodeParams{
		UserID:   uid,
		ParentID: folderNode.ID,
		Name:     "songB.mp3",
		IsDir:    false,
		DiskPath: pgtype.Text{String: "music/songB.mp3", Valid: true},
	})
	if err != nil {
		t.Fatalf("CreateNode (songB): %v", err)
	}

	folderID := db.UUIDString(folderNode.ID)
	count, err := ix.ScanFolder(ctx, userID, folderID)
	if err != nil {
		t.Fatalf("ScanFolder: %v", err)
	}
	if count != 2 {
		t.Errorf("ScanFolder returned count=%d, want 2", count)
	}

	// Assert both songs are in the DB with correct titles.
	sA, err := q.GetSongByNode(ctx, nodeA.ID)
	if err != nil {
		t.Fatalf("GetSongByNode(A): %v", err)
	}
	if sA.Title != "Song A" {
		t.Errorf("Song A title = %q, want %q", sA.Title, "Song A")
	}

	sB, err := q.GetSongByNode(ctx, nodeB.ID)
	if err != nil {
		t.Fatalf("GetSongByNode(B): %v", err)
	}
	if sB.Title != "Song B" {
		t.Errorf("Song B title = %q, want %q", sB.Title, "Song B")
	}

	// They should be under different artists/albums.
	if sA.ArtistID == sB.ArtistID {
		t.Errorf("expected different artist IDs for Song A and Song B")
	}
	if sA.AlbumID == sB.AlbumID {
		t.Errorf("expected different album IDs for Song A and Song B")
	}

	// A second ScanFolder should re-use cached rows (count returns 0 — nothing changed).
	count2, err := ix.ScanFolder(ctx, userID, folderID)
	if err != nil {
		t.Fatalf("ScanFolder (second): %v", err)
	}
	if count2 != 0 {
		t.Errorf("second ScanFolder: count=%d, want 0 (files unchanged)", count2)
	}
}

// The scan picks work with one query: a second scan of an unchanged folder indexes
// nothing, and a touched file (modified_at moved forward) is picked up again.
func TestScanFolderPicksOnlyStale(t *testing.T) {
	requireFFmpeg(t)
	q, ctx := setupDB(t)
	userID := makeTenant(t, q, ctx)
	uid, _ := db.ParseUUID(userID)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "music"), 0o755); err != nil {
		t.Fatal(err)
	}
	folder, _ := q.CreateNode(ctx, db.CreateNodeParams{UserID: uid, Name: "music", IsDir: true,
		DiskPath: pgtype.Text{String: "music", Valid: true}})
	var tracks []db.Node
	for _, name := range []string{"a.mp3", "b.mp3"} {
		synthesizeAudio(t, filepath.Join(root, "music", name), name, "Artist", "Album", "mp3")
		n, err := q.CreateNode(ctx, db.CreateNodeParams{UserID: uid, ParentID: folder.ID, Name: name,
			DiskPath: pgtype.Text{String: "music/" + name, Valid: true}})
		if err != nil {
			t.Fatal(err)
		}
		tracks = append(tracks, n)
	}
	ix := NewIndexer(q, root)
	fid := db.UUIDString(folder.ID)
	if n, err := ix.ScanFolder(ctx, userID, fid); err != nil || n != 2 {
		t.Fatalf("first scan: %d %v, want 2", n, err)
	}
	if n, err := ix.ScanFolder(ctx, userID, fid); err != nil || n != 0 {
		t.Fatalf("rescan of an unchanged folder indexed %d (%v), want 0", n, err)
	}
	if _, err := q.BumpNodeVersion(ctx, tracks[0].ID); err != nil { // modified_at = now()
		t.Fatal(err)
	}
	if n, err := ix.ScanFolder(ctx, userID, fid); err != nil || n != 1 {
		t.Fatalf("after touching one file: %d %v, want 1", n, err)
	}
}
