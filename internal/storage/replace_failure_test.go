package storage_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"discodrive/internal/db"
	"discodrive/internal/storage"
)

// hookDisk lets a test act right after a Move or Copy wrote a path, or fail a Copy.
type hookDisk struct {
	storage.Storage
	afterMove func(oldRel, newRel string) // after a successful Move or Copy to newRel
	failCopy  func(srcRel, dstRel string) error
	onRemove  func(rel string) // before a Remove
}

func (h *hookDisk) Remove(rel string) error {
	if h.onRemove != nil {
		h.onRemove(rel)
	}
	return h.Storage.Remove(rel)
}

func (h *hookDisk) Move(oldRel, newRel string) error {
	err := h.Storage.Move(oldRel, newRel)
	if err == nil && h.afterMove != nil {
		h.afterMove(oldRel, newRel)
	}
	return err
}

func (h *hookDisk) Copy(srcRel, dstRel string) error {
	if h.failCopy != nil {
		if err := h.failCopy(srcRel, dstRel); err != nil {
			return err
		}
	}
	err := h.Storage.Copy(srcRel, dstRel)
	if err == nil && h.afterMove != nil {
		h.afterMove(srcRel, dstRel)
	}
	return err
}

// replaceFixture: x.txt pushed as "one" (version 1), then overwritten with "two"
// (version 2), so version 1 is a snapshot a Restore can bring back.
type replaceFixture struct {
	fs     *storage.FileService
	q      *db.Queries
	pool   *pgxpool.Pool
	hook   *hookDisk
	root   string
	userID string
	node   db.Node // version 2, "two"
}

func newReplaceFixture(t *testing.T) replaceFixture {
	t.Helper()
	ctx := context.Background()
	hook := &hookDisk{}
	fs, q, pool, userID, root := setupFSPool(t, func(st storage.Storage) storage.Storage {
		hook.Storage = st
		return hook
	})
	if _, err := fs.Push(ctx, userID, nil, "x.txt", nil, "i", strings.NewReader("one")); err != nil {
		t.Fatalf("push one: %v", err)
	}
	res, err := fs.Push(ctx, userID, nil, "x.txt", nil, "i", strings.NewReader("two"))
	if err != nil {
		t.Fatalf("push two: %v", err)
	}
	if res.Node.Version != 2 {
		t.Fatalf("fixture: version %d, want 2", res.Node.Version)
	}
	return replaceFixture{fs: fs, q: q, pool: pool, hook: hook, root: root, userID: userID, node: res.Node}
}

func (f replaceFixture) live(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(f.root, f.node.DiskPath.String))
	if err != nil {
		t.Fatalf("read live file: %v", err)
	}
	return string(b)
}

// assertUnchanged: the live file still holds "two" and the row still describes it.
func (f replaceFixture) assertUnchanged(t *testing.T) {
	t.Helper()
	if got := f.live(t); got != "two" {
		t.Fatalf("live file holds %q after the failed write, want the previous content %q", got, "two")
	}
	n, err := f.q.GetNode(context.Background(), f.node.ID)
	if err != nil {
		t.Fatalf("node: %v", err)
	}
	if n.Version != f.node.Version || n.ContentHash != f.node.ContentHash {
		t.Fatalf("row changed: version %d hash %s, want %d %s", n.Version, n.ContentHash.String, f.node.Version, f.node.ContentHash.String)
	}
}

// The commit of a Restore fails after the restored bytes went into place (the client
// went away). The previous content must be back in the live file, and the row as it was.
func TestRestoreCommitFailureKeepsPreviousContent(t *testing.T) {
	f := newReplaceFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.hook.afterMove = func(_, newRel string) {
		if newRel == f.node.DiskPath.String {
			cancel()
		}
	}
	if _, err := f.fs.Restore(ctx, f.userID, db.UUIDString(f.node.ID), 1); err == nil {
		t.Fatalf("restore succeeded although its commit could not")
	}
	f.hook.afterMove = nil
	f.assertUnchanged(t)
}

// Same, and the previous content cannot be put back: the snapshot holding it must stay.
func TestRestoreCommitFailureKeepsSnapshotWhenPutBackFails(t *testing.T) {
	f := newReplaceFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	snapPrefix := ".versions/" + f.userID + "/" + db.UUIDString(f.node.ID) + "/2-"
	snapRel := ""
	f.hook.afterMove = func(_, newRel string) {
		if newRel == f.node.DiskPath.String {
			cancel()
		}
	}
	f.hook.failCopy = func(src, dst string) error {
		if strings.HasPrefix(src, snapPrefix) && dst == f.node.DiskPath.String {
			snapRel = src
			return errors.New("injected: disk full")
		}
		return nil
	}
	if _, err := f.fs.Restore(ctx, f.userID, db.UUIDString(f.node.ID), 1); err == nil {
		t.Fatalf("restore succeeded although its commit could not")
	}
	f.hook.afterMove, f.hook.failCopy = nil, nil
	if snapRel == "" {
		t.Fatalf("no put-back from a version 2 snapshot was attempted")
	}
	b, err := os.ReadFile(filepath.Join(f.root, snapRel))
	if err != nil || string(b) != "two" {
		t.Fatalf("snapshot of the replaced content = %q, %v; want it kept with %q", b, err, "two")
	}
}

// A Restore whose first attempt dies on a deadlock (injected on its change_log insert)
// is retried. The snapshot it takes of the content it replaces must hold that content,
// not bytes the failed attempt already wrote.
func TestRestoreRetriedAfterDeadlockKeepsVersionsRight(t *testing.T) {
	f := newReplaceFixture(t)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `
		CREATE SEQUENCE test_fail_once;
		CREATE FUNCTION test_fail_once() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF nextval('test_fail_once') = 1 THEN
				RAISE EXCEPTION 'injected deadlock' USING ERRCODE = '40P01';
			END IF;
			RETURN NEW;
		END $$;
		CREATE TRIGGER test_fail_once BEFORE INSERT ON change_log FOR EACH ROW EXECUTE FUNCTION test_fail_once();`); err != nil {
		t.Fatalf("trigger: %v", err)
	}
	restored, err := f.fs.Restore(ctx, f.userID, db.UUIDString(f.node.ID), 1)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := f.live(t); got != "one" {
		t.Fatalf("live file = %q after restoring version 1, want %q", got, "one")
	}
	if restored.Version != 3 {
		t.Fatalf("restored version %d, want 3", restored.Version)
	}
	fv, err := f.q.GetFileVersion(ctx, db.GetFileVersionParams{NodeID: f.node.ID, Version: 2})
	if err != nil {
		t.Fatalf("version 2 row: %v", err)
	}
	if fv.ContentHash != f.node.ContentHash {
		t.Fatalf("version 2 row hash %s, want %s", fv.ContentHash.String, f.node.ContentHash.String)
	}
	b, err := os.ReadFile(filepath.Join(f.root, fv.DiskPath.String))
	if err != nil || string(b) != "two" {
		t.Fatalf("version 2 snapshot holds %q (%v), want %q", b, err, "two")
	}
}

// The commit of an overwriting push fails after its bytes went into place: the
// previous content must be back.
func TestPushCommitFailureKeepsPreviousContent(t *testing.T) {
	f := newReplaceFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.hook.afterMove = func(_, newRel string) {
		if newRel == f.node.DiskPath.String {
			cancel()
		}
	}
	if _, err := f.fs.Push(ctx, f.userID, nil, "x.txt", nil, "i", strings.NewReader("three")); err == nil {
		t.Fatalf("push succeeded although its commit could not")
	}
	f.hook.afterMove = nil
	f.assertUnchanged(t)
}

// A push fails after it snapshotted version 2, and while it drops that snapshot a second
// push of the same file snapshots version 2 too and commits. The committed row's
// snapshot must survive the failed push's cleanup and hold the replaced content.
func TestFailedPushKeepsConcurrentPushSnapshot(t *testing.T) {
	f := newReplaceFixture(t)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `
		CREATE SEQUENCE test_fail_once;
		CREATE FUNCTION test_fail_once() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF nextval('test_fail_once') = 1 THEN
				RAISE EXCEPTION 'injected failure';
			END IF;
			RETURN NEW;
		END $$;
		CREATE TRIGGER test_fail_once BEFORE INSERT ON change_log FOR EACH ROW EXECUTE FUNCTION test_fail_once();`); err != nil {
		t.Fatalf("trigger: %v", err)
	}
	var otherErr error
	ran := false
	f.hook.onRemove = func(rel string) {
		if ran || !strings.HasPrefix(rel, ".versions/") {
			return
		}
		ran = true
		// The failed push's transaction is rolled back; the other push runs in full
		// between its check and its removal.
		_, otherErr = f.fs.Push(ctx, f.userID, nil, "x.txt", nil, "i", strings.NewReader("four"))
	}
	if _, err := f.fs.Push(ctx, f.userID, nil, "x.txt", nil, "i", strings.NewReader("three")); err == nil {
		t.Fatalf("push succeeded although its change_log insert failed")
	}
	f.hook.onRemove = nil
	if !ran {
		t.Fatalf("the failed push dropped no snapshot")
	}
	if otherErr != nil {
		t.Fatalf("concurrent push: %v", otherErr)
	}
	if got := f.live(t); got != "four" {
		t.Fatalf("live file = %q, want %q", got, "four")
	}
	fv, err := f.q.GetFileVersion(ctx, db.GetFileVersionParams{NodeID: f.node.ID, Version: 2})
	if err != nil {
		t.Fatalf("version 2 row: %v", err)
	}
	if fv.ContentHash != f.node.ContentHash {
		t.Fatalf("version 2 row hash %s, want %s", fv.ContentHash.String, f.node.ContentHash.String)
	}
	b, err := os.ReadFile(filepath.Join(f.root, fv.DiskPath.String))
	if err != nil || string(b) != "two" {
		t.Fatalf("version 2 snapshot at %s holds %q (%v), want %q", fv.DiskPath.String, b, err, "two")
	}
}
