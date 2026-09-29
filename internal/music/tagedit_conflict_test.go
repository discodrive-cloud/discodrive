package music

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/db"
	"discodrive/internal/music/tagwrite"
	"discodrive/internal/storage"
)

// A sync push that lands while tags are being edited must survive: the edit
// reports a conflict instead of overwriting the newer file, in both save modes.
func TestTagEditor_ConcurrentWriteIsNotClobbered(t *testing.T) {
	requireFFmpeg(t)
	for _, versioning := range []bool{true, false} {
		t.Run(map[bool]string{true: "versioned", false: "in-place"}[versioning], func(t *testing.T) {
			ctx := context.Background()
			fs, q, userID, root := setupTagEditorEnv(t)
			uid, _ := db.ParseUUID(userID)

			if err := storage.NewLocalDisk(root).Mkdir(userID + "/music"); err != nil {
				t.Fatal(err)
			}
			folder, err := q.CreateNode(ctx, db.CreateNodeParams{
				UserID: uid, Name: "music", IsDir: true,
				DiskPath: pgtype.Text{String: userID + "/music", Valid: true},
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := q.UpsertMusicSettings(ctx, db.UpsertMusicSettingsParams{
				UserID: uid, Enabled: true, FolderNodeID: folder.ID, TagEditVersioning: versioning,
			}); err != nil {
				t.Fatal(err)
			}
			songDisk := filepath.Join(root, userID, "music", "track.mp3")
			synthesizeAudio(t, songDisk, "Original", "Artist", "Album", "mp3")
			node, err := q.CreateNode(ctx, db.CreateNodeParams{
				UserID: uid, ParentID: folder.ID, Name: "track.mp3",
				DiskPath: pgtype.Text{String: userID + "/music/track.mp3", Valid: true},
			})
			if err != nil {
				t.Fatal(err)
			}

			concurrent := []byte("NEWER CONTENT FROM ANOTHER DEVICE")
			beforeTagCommit = func() {
				if _, err := fs.PushByPath(ctx, userID, "music/track.mp3", nil, bytes.NewReader(concurrent)); err != nil {
					t.Errorf("concurrent push: %v", err)
				}
			}
			defer func() { beforeTagCommit = func() {} }()

			ed := NewTagEditor(q, fs, root)
			err = ed.Write(ctx, userID, db.UUIDString(node.ID), tagwrite.Tags{Title: sp("Edited")}, tagwrite.CoverKeep, nil)
			if !errors.Is(err, ErrTagConflict) {
				t.Fatalf("Write = %v, want ErrTagConflict", err)
			}
			got, err := os.ReadFile(songDisk)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, concurrent) {
				t.Fatalf("the concurrent write was overwritten (%d bytes on disk)", len(got))
			}
		})
	}
}
