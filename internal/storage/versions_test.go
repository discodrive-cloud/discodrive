package storage_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/db"
)

// A file that is uploaded and never touched again must exist exactly once on disk.
// Snapshotting the content a push had just written stored every file twice — an
// upload of 1 GiB cost 2 GiB.
func TestVersions_FreshFileHasNoSnapshot(t *testing.T) {
	ctx := context.Background()
	fs, q, userID, root := setupFS(t)

	res, err := fs.Push(ctx, userID, nil, "once.txt", nil, "", strings.NewReader("hello"))
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	versions, err := q.ListFileVersions(ctx, res.Node.ID)
	if err != nil {
		t.Fatalf("list versions: %v", err)
	}
	if len(versions) != 0 {
		t.Fatalf("a first write must not be snapshotted, got %d version(s)", len(versions))
	}
	if _, err := os.Stat(filepath.Join(root, ".versions", userID, db.UUIDString(res.Node.ID))); !os.IsNotExist(err) {
		t.Fatalf("no snapshot directory should exist yet, stat = %v", err)
	}
}

// The content an overwrite replaces is what goes into the history — under the version
// it belonged to, so the rollback target is the old content and not the new one.
func TestVersions_OverwriteKeepsTheReplacedContent(t *testing.T) {
	ctx := context.Background()
	fs, q, userID, _ := setupFS(t)

	res, err := fs.Push(ctx, userID, nil, "f.txt", nil, "", strings.NewReader("first"))
	if err != nil {
		t.Fatalf("push 1: %v", err)
	}
	nodeID := db.UUIDString(res.Node.ID)
	if _, err := fs.Push(ctx, userID, nil, "f.txt", nil, "", strings.NewReader("second")); err != nil {
		t.Fatalf("push 2: %v", err)
	}

	versions, err := q.ListFileVersions(ctx, res.Node.ID)
	if err != nil {
		t.Fatalf("list versions: %v", err)
	}
	if len(versions) != 1 || versions[0].Version != 1 {
		t.Fatalf("expected one snapshot of v1, got %+v", versions)
	}
	if got := readNode(t, fs, userID, nodeID); got != "second" {
		t.Fatalf("live content = %q, want %q", got, "second")
	}

	// Rolling back to v1 returns the original content, and the revision being replaced
	// is preserved in turn — the rollback is itself undoable.
	if _, err := fs.Restore(ctx, userID, nodeID, 1); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := readNode(t, fs, userID, nodeID); got != "first" {
		t.Fatalf("after rollback content = %q, want %q", got, "first")
	}
	versions, err = q.ListFileVersions(ctx, res.Node.ID)
	if err != nil {
		t.Fatalf("list versions: %v", err)
	}
	if len(versions) != 2 {
		t.Fatalf("expected snapshots of v1 and v2 after the rollback, got %+v", versions)
	}
}

// Data written under the old scheme carries a snapshot of the content that is still
// live. The prune job is what gives that space back — without it the duplicates sit
// there until VERSION_KEEP rotation pushes them out, which for a file nobody edits is
// never. Past versions must survive it.
func TestVersions_PruneRemovesSnapshotsOfLiveContent(t *testing.T) {
	ctx := context.Background()
	fs, q, userID, root := setupFS(t)

	res, err := fs.Push(ctx, userID, nil, "f.txt", nil, "", strings.NewReader("first"))
	if err != nil {
		t.Fatalf("push 1: %v", err)
	}
	if _, err := fs.Push(ctx, userID, nil, "f.txt", nil, "", strings.NewReader("second")); err != nil {
		t.Fatalf("push 2: %v", err)
	}
	nodeID := db.UUIDString(res.Node.ID)

	// Recreate what the old scheme left behind: a snapshot of the current version.
	snapDir := filepath.Join(root, ".versions", userID, nodeID)
	if err := os.MkdirAll(snapDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	livePath := filepath.Join(snapDir, "2")
	if err := os.WriteFile(livePath, []byte("second"), 0o644); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}
	if _, err := q.InsertFileVersion(ctx, db.InsertFileVersionParams{
		NodeID: res.Node.ID, Version: 2, DiskPath: pgtype.Text{String: ".versions/" + userID + "/" + nodeID + "/2", Valid: true},
	}); err != nil {
		t.Fatalf("insert live snapshot: %v", err)
	}

	// idleFor 0: everything written before this instant qualifies.
	if err := fs.PruneLiveSnapshots(ctx, 0); err != nil {
		t.Fatalf("prune: %v", err)
	}

	versions, err := q.ListFileVersions(ctx, res.Node.ID)
	if err != nil {
		t.Fatalf("list versions: %v", err)
	}
	if len(versions) != 1 || versions[0].Version != 1 {
		t.Fatalf("only the snapshot of v1 must survive, got %+v", versions)
	}
	if _, err := os.Stat(livePath); !os.IsNotExist(err) {
		t.Fatalf("the redundant snapshot file must be deleted, stat = %v", err)
	}
	// The real history is untouched and still restorable.
	if _, err := fs.Restore(ctx, userID, nodeID, 1); err != nil {
		t.Fatalf("restore after prune: %v", err)
	}
	if got := readNode(t, fs, userID, nodeID); got != "first" {
		t.Fatalf("restored content = %q, want %q", got, "first")
	}
}

// VERSION_KEEP=0 turns history off entirely: overwrites replace the file and nothing
// accumulates on disk.
func TestVersions_DisabledKeepsNothing(t *testing.T) {
	ctx := context.Background()
	fs, q, userID, root := setupFS(t)
	fs.DisableVersions()

	res, err := fs.Push(ctx, userID, nil, "f.txt", nil, "", strings.NewReader("first"))
	if err != nil {
		t.Fatalf("push 1: %v", err)
	}
	if _, err := fs.Push(ctx, userID, nil, "f.txt", nil, "", strings.NewReader("second")); err != nil {
		t.Fatalf("push 2: %v", err)
	}

	versions, err := q.ListFileVersions(ctx, res.Node.ID)
	if err != nil {
		t.Fatalf("list versions: %v", err)
	}
	if len(versions) != 0 {
		t.Fatalf("versioning is off, got %d version(s)", len(versions))
	}
	if _, err := os.Stat(filepath.Join(root, ".versions", userID)); !os.IsNotExist(err) {
		t.Fatalf("nothing should be written under .versions, stat = %v", err)
	}
	if got := readNode(t, fs, userID, db.UUIDString(res.Node.ID)); got != "second" {
		t.Fatalf("live content = %q, want %q", got, "second")
	}
}
