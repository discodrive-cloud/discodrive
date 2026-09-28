package music

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/db"
)

// A track moved to the trash stayed in the library (and in Subsonic clients) until the
// trash was purged, up to TRASH_DAYS later.
func TestTrashedTrackLeavesLibrary(t *testing.T) {
	t.Skip("bug confirmed; fixed and re-asserted in internal/library (task 2.3)")
	requireFFmpeg(t)
	q, ctx := setupDB(t)
	userID := makeTenant(t, q, ctx)
	uid, _ := db.ParseUUID(userID)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "music"), 0o755); err != nil {
		t.Fatal(err)
	}
	synthesizeAudio(t, filepath.Join(root, "music", "a.mp3"), "A", "Artist", "Album", "mp3")
	folder, err := q.CreateNode(ctx, db.CreateNodeParams{UserID: uid, Name: "music", IsDir: true,
		DiskPath: pgtype.Text{String: "music", Valid: true}})
	if err != nil {
		t.Fatal(err)
	}
	track, err := q.CreateNode(ctx, db.CreateNodeParams{UserID: uid, ParentID: folder.ID, Name: "a.mp3",
		DiskPath: pgtype.Text{String: "music/a.mp3", Valid: true}})
	if err != nil {
		t.Fatal(err)
	}
	ix := NewIndexer(q, root)
	if _, err := ix.ScanFolder(ctx, userID, db.UUIDString(folder.ID)); err != nil {
		t.Fatal(err)
	}
	if songs, _ := q.AccessibleSongs(ctx, uid); len(songs) != 1 {
		t.Fatalf("setup: %d songs indexed, want 1", len(songs))
	}

	if err := q.SoftDeleteNode(ctx, track.ID); err != nil {
		t.Fatal(err)
	}
	catchUpAfterTrash(t, q, ix, uid) // no-op until task 2.3

	if songs, _ := q.AccessibleSongs(ctx, uid); len(songs) != 0 {
		t.Fatalf("a trashed track is still in the library (%d songs)", len(songs))
	}
}

func catchUpAfterTrash(*testing.T, *db.Queries, *Indexer, pgtype.UUID) {}
