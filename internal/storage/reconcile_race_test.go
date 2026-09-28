package storage_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"discodrive/internal/db"
)

// An upload committing while reconciliation re-hashes the same file must win: the
// reconciliation read the node, the upload bumped it, and an unconditional update
// used to overwrite the upload's size and hash with ones computed from older content.
func TestReconcileDoesNotOverwriteConcurrentUpload(t *testing.T) {
	fs, q, pool, userID, root := setupFSPool(t, nil)
	ctx := context.Background()
	uid, _ := db.ParseUUID(userID)
	res, err := fs.Push(ctx, userID, nil, "a.txt", nil, "", strings.NewReader("12345"))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, res.Node.DiskPath.String), "123456789") // size differs: reconcile will re-hash

	tx, err := pool.Begin(ctx) // stands in for an upload holding the row mid-commit
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE nodes SET size = 42, content_hash = 'upload', version = version + 1 WHERE id = $1`, res.Node.ID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := fs.ReconcileUser(ctx, uid); done <- err }()
	time.Sleep(500 * time.Millisecond) // let reconciliation reach the locked row
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	n, err := q.GetNode(ctx, res.Node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if n.Size.Int64 != 42 || n.ContentHash.String != "upload" {
		t.Fatalf("reconciliation overwrote the upload: size %d hash %q", n.Size.Int64, n.ContentHash.String)
	}
}
