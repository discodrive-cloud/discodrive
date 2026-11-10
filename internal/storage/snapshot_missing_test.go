package storage_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"discodrive/internal/db"
)

// A file that vanished from the disk while its node is still live (removed on the NAS
// directly, no rescan yet) has nothing to snapshot. Pushing the same path again must put
// the new content in place rather than fail on the missing old one in every sync cycle.
func TestPushOverFileMissingFromDisk(t *testing.T) {
	fs, _, userID, root := setupFS(t)
	ctx := context.Background()
	first, err := fs.Push(ctx, userID, nil, "note.txt", nil, "dev", strings.NewReader("first"))
	if err != nil {
		t.Fatalf("first push: %v", err)
	}
	if err := os.Remove(filepath.Join(root, first.Node.DiskPath.String)); err != nil {
		t.Fatalf("remove the live file: %v", err)
	}

	second, err := fs.Push(ctx, userID, nil, "note.txt", i64(first.Node.Version), "dev", strings.NewReader("second"))
	if err != nil {
		t.Fatalf("push over the missing file: %v", err)
	}
	if second.Conflicted {
		t.Fatal("the push became a conflict copy")
	}
	if got := readNode(t, fs, userID, db.UUIDString(second.Node.ID)); got != "second" {
		t.Fatalf("content = %q, want %q", got, "second")
	}
	versions, err := fs.ListVersions(ctx, userID, db.UUIDString(second.Node.ID))
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 0 {
		t.Errorf("%d versions recorded for content that no longer existed", len(versions))
	}
}
