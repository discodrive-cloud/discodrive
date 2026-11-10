package storage_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"discodrive/internal/db"
)

// A file put on the disk directly (over SMB, on the NAS) is unknown to the database
// until a rescan. An upload to the same name used to replace it without a trace; it is
// now kept next to the upload under another name, for the rescan to pick up.
func TestUploadDoesNotDestroyFileUnknownToTheDatabase(t *testing.T) {
	fs, _, userID, root := setupFS(t)
	ctx := context.Background()
	// The user's directory appears with the first upload.
	if _, err := fs.Push(ctx, userID, nil, "other.txt", nil, "dev", strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, userID, "report.pdf"), "placed on the NAS by hand")

	res, err := fs.Push(ctx, userID, nil, "report.pdf", nil, "dev", strings.NewReader("uploaded"))
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if got := readNode(t, fs, userID, db.UUIDString(res.Node.ID)); got != "uploaded" {
		t.Fatalf("uploaded content = %q", got)
	}
	entries, err := os.ReadDir(filepath.Join(root, userID))
	if err != nil {
		t.Fatal(err)
	}
	var kept string
	for _, e := range entries {
		b, _ := os.ReadFile(filepath.Join(root, userID, e.Name()))
		if string(b) == "placed on the NAS by hand" {
			kept = e.Name()
		}
	}
	if kept == "" {
		t.Fatal("the file that was on the disk is gone")
	}
	if kept == "report.pdf" || !strings.HasSuffix(kept, ".pdf") {
		t.Errorf("the disk file was kept as %q, want another .pdf name", kept)
	}
}

// kept returns the name under which a file with this content sits in dir, or "".
func kept(t *testing.T, dir, content string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if b, _ := os.ReadFile(filepath.Join(dir, e.Name())); string(b) == content {
			return e.Name()
		}
	}
	return ""
}

// Renaming, moving and restoring from the trash land a file on a path too; a file sitting
// there unknown to the database is kept the same way.
func TestRenameMoveUndeleteKeepFilesUnknownToTheDatabase(t *testing.T) {
	fs, _, userID, root := setupFS(t)
	ctx := context.Background()
	home := filepath.Join(root, userID)
	push := func(name string) string {
		res, err := fs.Push(ctx, userID, nil, name, nil, "dev", strings.NewReader("node "+name))
		if err != nil {
			t.Fatalf("push %s: %v", name, err)
		}
		return db.UUIDString(res.Node.ID)
	}

	a := push("a.txt")
	writeFile(t, filepath.Join(home, "renamed.txt"), "stray at the rename target")
	if _, err := fs.Rename(ctx, userID, a, "renamed.txt"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if got := readNode(t, fs, userID, a); got != "node a.txt" {
		t.Fatalf("renamed node reads %q", got)
	}
	if name := kept(t, home, "stray at the rename target"); name == "" || name == "renamed.txt" {
		t.Errorf("rename: the disk file was not kept (found as %q)", name)
	}

	folder, err := fs.CreateFolder(ctx, userID, nil, "dir")
	if err != nil {
		t.Fatal(err)
	}
	folderID := db.UUIDString(folder.ID)
	b := push("b.txt")
	writeFile(t, filepath.Join(home, "dir", "b.txt"), "stray at the move target")
	if _, err := fs.Move(ctx, userID, b, &folderID); err != nil {
		t.Fatalf("move: %v", err)
	}
	if got := readNode(t, fs, userID, b); got != "node b.txt" {
		t.Fatalf("moved node reads %q", got)
	}
	if name := kept(t, filepath.Join(home, "dir"), "stray at the move target"); name == "" || name == "b.txt" {
		t.Errorf("move: the disk file was not kept (found as %q)", name)
	}

	c := push("c.txt")
	if err := fs.Delete(ctx, userID, c); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(home, "c.txt"), "stray at the restore target")
	if _, err := fs.Undelete(ctx, userID, c); err != nil {
		t.Fatalf("undelete: %v", err)
	}
	if got := readNode(t, fs, userID, c); got != "node c.txt" {
		t.Fatalf("restored node reads %q", got)
	}
	if name := kept(t, home, "stray at the restore target"); name == "" || name == "c.txt" {
		t.Errorf("undelete: the disk file was not kept (found as %q)", name)
	}

	// A change of letter case only is the same file on a case-insensitive disk: it must
	// still be a plain rename.
	d := push("d.txt")
	if _, err := fs.Rename(ctx, userID, d, "D.txt"); err != nil {
		t.Fatalf("case-only rename: %v", err)
	}
	if got := readNode(t, fs, userID, d); got != "node d.txt" {
		t.Fatalf("case-renamed node reads %q", got)
	}
}
