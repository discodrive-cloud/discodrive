package storage_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"discodrive/internal/db"
	"discodrive/internal/quota"
	"discodrive/internal/storage"
)

func exists(t *testing.T, p string) bool {
	t.Helper()
	_, err := os.Lstat(p)
	if err == nil {
		return true
	}
	if !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return false
}

func childByName(t *testing.T, fs *storage.FileService, userID, dirID, name string) db.Node {
	t.Helper()
	kids, err := fs.ListChildren(context.Background(), userID, dirID)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range kids {
		if k.Name == name {
			return k
		}
	}
	t.Fatalf("no %q in folder", name)
	return db.Node{}
}

// Deleting moves the bytes out of the tree, so uploading the same name again cannot
// overwrite what the trash holds; restoring brings back the old bytes and versions.
func TestTrashKeepsContentWhenNameIsReused(t *testing.T) {
	ctx := context.Background()
	fs, _, userID, root := setupFS(t)

	v1, err := fs.Push(ctx, userID, nil, "x.txt", nil, "", strings.NewReader("old v1"))
	if err != nil {
		t.Fatal(err)
	}
	old, err := fs.Push(ctx, userID, nil, "x.txt", nil, "", strings.NewReader("old v2"))
	if err != nil {
		t.Fatal(err)
	}
	oldID := db.UUIDString(old.Node.ID)
	if err := fs.Delete(ctx, userID, oldID); err != nil {
		t.Fatal(err)
	}
	trashed := filepath.Join(root, ".trash", userID, oldID)
	if exists(t, filepath.Join(root, userID, "x.txt")) || !exists(t, trashed) {
		t.Fatal("delete must move the bytes to .trash/<user>/<node>")
	}
	live, err := fs.Push(ctx, userID, nil, "x.txt", nil, "", strings.NewReader("new"))
	if err != nil {
		t.Fatal(err)
	}
	restored, err := fs.Undelete(ctx, userID, oldID)
	if err != nil {
		t.Fatalf("undelete: %v", err)
	}
	if restored.Name != "x.txt (restored)" {
		t.Fatalf("restored as %q", restored.Name)
	}
	if got := readNode(t, fs, userID, oldID); got != "old v2" {
		t.Fatalf("restored content %q, want the trashed bytes", got)
	}
	if got := readNode(t, fs, userID, db.UUIDString(live.Node.ID)); got != "new" {
		t.Fatalf("live content %q", got)
	}
	if exists(t, trashed) {
		t.Fatal("the trash directory must be empty after restoring")
	}
	// Version history follows the node through the trash.
	if _, err := fs.Restore(ctx, userID, oldID, v1.Node.Version); err != nil {
		t.Fatalf("restore version: %v", err)
	}
	if got := readNode(t, fs, userID, oldID); got != "old v1" {
		t.Fatalf("version 1 content %q", got)
	}
}

// A folder deleted, created again and deleted again leaves two trashed trees with the
// same paths. Each must restore and purge on its own.
func TestTwoTrashedTreesWithTheSamePath(t *testing.T) {
	ctx := context.Background()
	fs, _, userID, root := setupFS(t)

	mk := func(file, body string) (db.Node, db.Node) {
		dir, err := fs.CreateFolder(ctx, userID, nil, "docs")
		if err != nil {
			t.Fatal(err)
		}
		did := db.UUIDString(dir.ID)
		f, err := fs.Push(ctx, userID, &did, file, nil, "", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if err := fs.Delete(ctx, userID, did); err != nil {
			t.Fatal(err)
		}
		return dir, f.Node
	}
	first, firstFile := mk("a.txt", "one")
	second, secondFile := mk("b.txt", "two")

	r1, err := fs.Undelete(ctx, userID, db.UUIDString(first.ID))
	if err != nil {
		t.Fatalf("undelete first: %v", err)
	}
	kids, err := fs.ListChildren(ctx, userID, db.UUIDString(r1.ID))
	if err != nil {
		t.Fatal(err)
	}
	if len(kids) != 1 || kids[0].ID != firstFile.ID {
		t.Fatalf("first tree restored with %d children, want only a.txt", len(kids))
	}
	if got := readNode(t, fs, userID, db.UUIDString(firstFile.ID)); got != "one" {
		t.Fatalf("a.txt = %q", got)
	}

	// Purging the second tree leaves the restored first one alone, on disk too.
	if err := fs.Purge(ctx, userID, db.UUIDString(second.ID)); err != nil {
		t.Fatalf("purge second: %v", err)
	}
	if got := readNode(t, fs, userID, db.UUIDString(firstFile.ID)); got != "one" {
		t.Fatalf("a.txt after purging the other tree = %q", got)
	}
	if exists(t, filepath.Join(root, ".trash", userID, db.UUIDString(second.ID))) {
		t.Fatal("purge must remove the trashed bytes")
	}
	if _, err := fs.Undelete(ctx, userID, db.UUIDString(secondFile.ID)); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("purged tree's file must be gone, got %v", err)
	}
	trash, err := fs.Trash(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if len(trash) != 0 {
		t.Fatalf("trash still lists %d items", len(trash))
	}
}

// The same with both trees still in the trash: restoring one of them must not
// restore rows of the other, and the second restore gets a free name.
func TestUndeleteOneOfTwoSamePathTreesThenTheOther(t *testing.T) {
	ctx := context.Background()
	fs, _, userID, _ := setupFS(t)

	var dirs []db.Node
	var files []db.Node
	for _, body := range []string{"one", "two"} {
		dir, err := fs.CreateFolder(ctx, userID, nil, "docs")
		if err != nil {
			t.Fatal(err)
		}
		did := db.UUIDString(dir.ID)
		f, err := fs.Push(ctx, userID, &did, "same.txt", nil, "", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if err := fs.Delete(ctx, userID, did); err != nil {
			t.Fatal(err)
		}
		dirs, files = append(dirs, dir), append(files, f.Node)
	}
	a, err := fs.Undelete(ctx, userID, db.UUIDString(dirs[1].ID))
	if err != nil {
		t.Fatalf("undelete second: %v", err)
	}
	b, err := fs.Undelete(ctx, userID, db.UUIDString(dirs[0].ID))
	if err != nil {
		t.Fatalf("undelete first: %v", err)
	}
	if a.Name != "docs" || b.Name != "docs (restored)" {
		t.Fatalf("restored as %q and %q", a.Name, b.Name)
	}
	if got := readNode(t, fs, userID, db.UUIDString(files[1].ID)); got != "two" {
		t.Fatalf("second tree's file = %q", got)
	}
	if got := readNode(t, fs, userID, db.UUIDString(files[0].ID)); got != "one" {
		t.Fatalf("first tree's file = %q", got)
	}
	if n := childByName(t, fs, userID, db.UUIDString(b.ID), "same.txt"); n.ID != files[0].ID {
		t.Fatal("first tree's file is not under its own restored folder")
	}
}

// A file restored on its own out of a trashed folder survives purging that folder.
func TestPurgeFolderAfterRestoringOneOfItsFiles(t *testing.T) {
	ctx := context.Background()
	fs, _, userID, _ := setupFS(t)

	dir, err := fs.CreateFolder(ctx, userID, nil, "docs")
	if err != nil {
		t.Fatal(err)
	}
	did := db.UUIDString(dir.ID)
	keep, err := fs.Push(ctx, userID, &did, "keep.txt", nil, "", strings.NewReader("keep"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Push(ctx, userID, &did, "drop.txt", nil, "", strings.NewReader("drop")); err != nil {
		t.Fatal(err)
	}
	if err := fs.Delete(ctx, userID, did); err != nil {
		t.Fatal(err)
	}
	kid := db.UUIDString(keep.Node.ID)
	restored, err := fs.Undelete(ctx, userID, kid)
	if err != nil {
		t.Fatalf("undelete file: %v", err)
	}
	if restored.ParentID.Valid {
		t.Fatal("a file whose folder is trashed is restored to the root")
	}
	if err := fs.Purge(ctx, userID, did); err != nil {
		t.Fatalf("purge folder: %v", err)
	}
	if got := readNode(t, fs, userID, kid); got != "keep" {
		t.Fatalf("restored file after purging its old folder = %q", got)
	}
}

// Deleting a file and later its folder trashes them separately. Restoring the folder
// brings back what was deleted with it; the file deleted earlier stays in the trash,
// and purging the folder removes that file's own trash directory too.
func TestSeparatelyTrashedChild(t *testing.T) {
	ctx := context.Background()
	fs, _, userID, root := setupFS(t)

	dir, err := fs.CreateFolder(ctx, userID, nil, "docs")
	if err != nil {
		t.Fatal(err)
	}
	did := db.UUIDString(dir.ID)
	early, err := fs.Push(ctx, userID, &did, "early.txt", nil, "", strings.NewReader("early"))
	if err != nil {
		t.Fatal(err)
	}
	late, err := fs.Push(ctx, userID, &did, "late.txt", nil, "", strings.NewReader("late"))
	if err != nil {
		t.Fatal(err)
	}
	eid := db.UUIDString(early.Node.ID)
	if err := fs.Delete(ctx, userID, eid); err != nil {
		t.Fatal(err)
	}
	if err := fs.Delete(ctx, userID, did); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Undelete(ctx, userID, did); err != nil {
		t.Fatalf("undelete folder: %v", err)
	}
	if got := readNode(t, fs, userID, db.UUIDString(late.Node.ID)); got != "late" {
		t.Fatalf("late.txt = %q", got)
	}
	trash, err := fs.Trash(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if len(trash) != 1 || trash[0].ID != early.Node.ID {
		t.Fatalf("trash should list early.txt alone, has %d", len(trash))
	}
	if got := readNode(t, fs, userID, db.UUIDString(late.Node.ID)); got != "late" {
		t.Fatal(got)
	}
	// Trash the folder again and purge it: early.txt goes with it, bytes included.
	if err := fs.Delete(ctx, userID, did); err != nil {
		t.Fatal(err)
	}
	if err := fs.Purge(ctx, userID, did); err != nil {
		t.Fatal(err)
	}
	if exists(t, filepath.Join(root, ".trash", userID, eid)) {
		t.Fatal("purging the folder must remove the separately trashed child's bytes")
	}
	if entries, err := os.ReadDir(filepath.Join(root, ".trash", userID)); err == nil && len(entries) != 0 {
		t.Fatalf("trash directory not empty: %d entries", len(entries))
	}
}

// Rescan never looks into .trash, and a file placed on disk under a trashed name is new
// content: it is imported, and the trash still restores its own bytes.
func TestRescanWithNewTrashLayout(t *testing.T) {
	ctx := context.Background()
	fs, _, userID, root := setupFS(t)

	n, err := fs.Push(ctx, userID, nil, "x.txt", nil, "", strings.NewReader("trashed"))
	if err != nil {
		t.Fatal(err)
	}
	id := db.UUIDString(n.Node.ID)
	if err := fs.Delete(ctx, userID, id); err != nil {
		t.Fatal(err)
	}
	if err := fs.Rescan(ctx); err != nil {
		t.Fatal(err)
	}
	if names := liveNames(t, fs, userID); len(names) != 0 {
		t.Fatalf("rescan imported %v", names)
	}
	if err := os.WriteFile(filepath.Join(root, userID, "x.txt"), []byte("dropped in"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := fs.Rescan(ctx); err != nil {
		t.Fatal(err)
	}
	if names := liveNames(t, fs, userID); len(names) != 1 {
		t.Fatalf("a file placed under a trashed name must be imported, live: %v", names)
	}
	if _, err := fs.Undelete(ctx, userID, id); err != nil {
		t.Fatal(err)
	}
	if got := readNode(t, fs, userID, id); got != "trashed" {
		t.Fatalf("restored = %q", got)
	}
}

// Tombstones written before deletes moved bytes (trash_path NULL, bytes at disk_path)
// keep working: restore, GC and the quota check on the copy they may need.
func TestLegacyTombstones(t *testing.T) {
	ctx := context.Background()
	fs, q, pool, userID, root := setupFSPool(t, nil)
	legacyDelete := func(n db.Node) {
		t.Helper()
		if _, err := pool.Exec(ctx, `UPDATE nodes SET deleted_at = now() WHERE id = $1`, n.ID); err != nil {
			t.Fatal(err)
		}
	}

	a, err := fs.Push(ctx, userID, nil, "a.txt", nil, "", strings.NewReader("aaaa"))
	if err != nil {
		t.Fatal(err)
	}
	legacyDelete(a.Node)
	if _, err := fs.Undelete(ctx, userID, db.UUIDString(a.Node.ID)); err != nil {
		t.Fatalf("undelete legacy: %v", err)
	}
	if got := readNode(t, fs, userID, db.UUIDString(a.Node.ID)); got != "aaaa" {
		t.Fatalf("legacy restore = %q", got)
	}

	// The name reused over a legacy tombstone: its bytes belong to the live file, so a
	// restore copies them — and a copy is a write the quota must allow.
	legacyDelete(a.Node)
	if _, err := fs.Push(ctx, userID, nil, "a.txt", nil, "", strings.NewReader("bbbb")); err != nil {
		t.Fatal(err)
	}
	fs.SetQuota(quota.New(q, 0))
	setQuota(t, q, userID, 10) // 8 used: the live file and the tombstone
	if _, err := fs.Undelete(ctx, userID, db.UUIDString(a.Node.ID)); !errors.Is(err, quota.ErrExceeded) {
		t.Fatalf("legacy copy restore over quota: want ErrExceeded, got %v", err)
	}
	setQuota(t, q, userID, 100)
	if _, err := fs.Undelete(ctx, userID, db.UUIDString(a.Node.ID)); err != nil {
		t.Fatalf("legacy copy restore: %v", err)
	}

	// GC of a legacy tombstone removes its bytes at disk_path.
	g, err := fs.Push(ctx, userID, nil, "g.txt", nil, "", strings.NewReader("g"))
	if err != nil {
		t.Fatal(err)
	}
	legacyDelete(g.Node)
	if err := fs.TrashGC(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if exists(t, filepath.Join(root, userID, "g.txt")) {
		t.Fatal("GC must remove a legacy tombstone's bytes")
	}
}

// Trash GC removes the moved bytes and never touches a live file at the old path.
func TestTrashGCWithNewLayout(t *testing.T) {
	ctx := context.Background()
	fs, _, userID, root := setupFS(t)

	dir, err := fs.CreateFolder(ctx, userID, nil, "docs")
	if err != nil {
		t.Fatal(err)
	}
	did := db.UUIDString(dir.ID)
	if _, err := fs.Push(ctx, userID, &did, "a.txt", nil, "", strings.NewReader("a")); err != nil {
		t.Fatal(err)
	}
	if err := fs.Delete(ctx, userID, did); err != nil {
		t.Fatal(err)
	}
	again, err := fs.CreateFolder(ctx, userID, nil, "docs")
	if err != nil {
		t.Fatal(err)
	}
	aid := db.UUIDString(again.ID)
	live, err := fs.Push(ctx, userID, &aid, "a.txt", nil, "", strings.NewReader("live"))
	if err != nil {
		t.Fatal(err)
	}
	if err := fs.TrashGC(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if exists(t, filepath.Join(root, ".trash", userID, did)) {
		t.Fatal("GC must remove the trashed bytes")
	}
	if got := readNode(t, fs, userID, db.UUIDString(live.Node.ID)); got != "live" {
		t.Fatalf("live file after GC = %q", got)
	}
}

// A node whose bytes are already gone from disk can still be trashed and restored.
func TestDeleteAndRestoreWithoutBytes(t *testing.T) {
	ctx := context.Background()
	fs, _, userID, root := setupFS(t)

	n, err := fs.Push(ctx, userID, nil, "gone.txt", nil, "", strings.NewReader("x"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, userID, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	id := db.UUIDString(n.Node.ID)
	if err := fs.Delete(ctx, userID, id); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := fs.Undelete(ctx, userID, id); err != nil {
		t.Fatalf("undelete: %v", err)
	}
}
