package storage_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"discodrive/internal/db"
	"discodrive/internal/storage"
)

// Soft delete leaves the bytes at the tombstone's path; a file uploaded again under the
// same name lands on that path. Trash GC must not remove it when the tombstone expires.
func TestTrashGCKeepsLiveFileAtReusedPath(t *testing.T) {
	ctx := context.Background()
	fs, _, userID, _ := setupFS(t)

	old, err := fs.Push(ctx, userID, nil, "x.txt", nil, "i", strings.NewReader("old"))
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if err := fs.Delete(ctx, userID, db.UUIDString(old.Node.ID)); err != nil {
		t.Fatalf("delete: %v", err)
	}
	live, err := fs.Push(ctx, userID, nil, "x.txt", nil, "i", strings.NewReader("new"))
	if err != nil {
		t.Fatalf("push again: %v", err)
	}
	if err := fs.TrashGC(ctx, 0); err != nil {
		t.Fatalf("gc: %v", err)
	}
	if got := readNode(t, fs, userID, db.UUIDString(live.Node.ID)); got != "new" {
		t.Fatalf("live file after GC = %q, want %q", got, "new")
	}
}

func TestTrashGCKeepsLiveFolderAtReusedPath(t *testing.T) {
	ctx := context.Background()
	fs, _, userID, _ := setupFS(t)

	dir, err := fs.CreateFolder(ctx, userID, nil, "docs")
	if err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	did := db.UUIDString(dir.ID)
	if _, err := fs.Push(ctx, userID, &did, "old.txt", nil, "i", strings.NewReader("old")); err != nil {
		t.Fatalf("push: %v", err)
	}
	if err := fs.Delete(ctx, userID, did); err != nil {
		t.Fatalf("delete: %v", err)
	}
	again, err := fs.CreateFolder(ctx, userID, nil, "docs")
	if err != nil {
		t.Fatalf("mkdir again: %v", err)
	}
	aid := db.UUIDString(again.ID)
	live, err := fs.Push(ctx, userID, &aid, "live.txt", nil, "i", strings.NewReader("keep me"))
	if err != nil {
		t.Fatalf("push live: %v", err)
	}
	if err := fs.TrashGC(ctx, 0); err != nil {
		t.Fatalf("gc: %v", err)
	}
	if got := readNode(t, fs, userID, db.UUIDString(live.Node.ID)); got != "keep me" {
		t.Fatalf("live file in reused folder after GC = %q", got)
	}
}

// Without a live node at the path, GC still frees the bytes.
func TestTrashGCRemovesUnusedPath(t *testing.T) {
	ctx := context.Background()
	fs, _, userID, root := setupFS(t)

	n, err := fs.Push(ctx, userID, nil, "gone.txt", nil, "i", strings.NewReader("x"))
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if err := fs.Delete(ctx, userID, db.UUIDString(n.Node.ID)); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := fs.TrashGC(ctx, 0); err != nil {
		t.Fatalf("gc: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, userID, "gone.txt")); !os.IsNotExist(err) {
		t.Fatalf("expired tombstone still on disk: %v", err)
	}
}

// "_" and "%" are ordinary characters in names. Subtree operations on a_b must not
// touch its neighbour aXb (a LIKE pattern would match it: "_" = any one character).
func TestSubtreeOpsTreatWildcardsLiterally(t *testing.T) {
	ctx := context.Background()
	fs, _, userID, _ := setupFS(t)

	ab, err := fs.CreateFolder(ctx, userID, nil, "a_b")
	if err != nil {
		t.Fatalf("mkdir a_b: %v", err)
	}
	axb, err := fs.CreateFolder(ctx, userID, nil, "aXb")
	if err != nil {
		t.Fatalf("mkdir aXb: %v", err)
	}
	axbID := db.UUIDString(axb.ID)
	neighbour, err := fs.Push(ctx, userID, &axbID, "f.txt", nil, "i", strings.NewReader("neighbour"))
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	pct, err := fs.CreateFolder(ctx, userID, nil, "100%")
	if err != nil {
		t.Fatalf("mkdir 100%%: %v", err)
	}
	other, err := fs.CreateFolder(ctx, userID, nil, "100 percent")
	if err != nil {
		t.Fatalf("mkdir 100 percent: %v", err)
	}
	otherID := db.UUIDString(other.ID)
	if _, err := fs.Push(ctx, userID, &otherID, "g.txt", nil, "i", strings.NewReader("other")); err != nil {
		t.Fatalf("push: %v", err)
	}

	// Rename rewrites the subtree paths of a_b only.
	abID := db.UUIDString(ab.ID)
	if _, err := fs.Rename(ctx, userID, abID, "renamed"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if got := readNode(t, fs, userID, db.UUIDString(neighbour.Node.ID)); got != "neighbour" {
		t.Fatalf("neighbour after rename = %q", got)
	}
	// Delete trashes the subtree of a folder only.
	if err := fs.Delete(ctx, userID, abID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := fs.Delete(ctx, userID, db.UUIDString(pct.ID)); err != nil {
		t.Fatalf("delete 100%%: %v", err)
	}
	for _, p := range []string{"/aXb/f.txt", "/100 percent/g.txt"} {
		if _, err := fs.NodeByPath(ctx, userID, p); err != nil {
			t.Fatalf("%s went to the trash with its neighbour: %v", p, err)
		}
	}
}

func TestAdoptCreatesNodeForStagedFile(t *testing.T) {
	ctx := context.Background()
	fs, _, userID, root := setupFS(t)

	stage := func(name, content string) (string, int64, string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(root, ".tmp"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, ".tmp", name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte(content))
		return ".tmp/" + name, int64(len(content)), hex.EncodeToString(sum[:])
	}

	tmp, size, hash := stage("a", "payload")
	node, err := fs.Adopt(ctx, userID, tmp, userID+"/Downloads/file.bin", size, hash)
	if err != nil {
		t.Fatalf("adopt: %v", err)
	}
	byPath, err := fs.NodeByPath(ctx, userID, "/Downloads/file.bin")
	if err != nil || byPath.ID != node.ID {
		t.Fatalf("adopted file has no node at its path: %v", err)
	}
	if byPath.Size.Int64 != size || byPath.ContentHash.String != hash {
		t.Fatalf("node size/hash = %d/%s, want %d/%s", byPath.Size.Int64, byPath.ContentHash.String, size, hash)
	}
	if got := readNode(t, fs, userID, db.UUIDString(node.ID)); got != "payload" {
		t.Fatalf("content = %q", got)
	}

	// A taken destination is refused and the staged bytes stay where they were.
	tmp2, size2, hash2 := stage("b", "second")
	if _, err := fs.Adopt(ctx, userID, tmp2, userID+"/Downloads/file.bin", size2, hash2); !errors.Is(err, storage.ErrNameTaken) {
		t.Fatalf("adopt onto a taken name: err = %v, want ErrNameTaken", err)
	}
	if got := readNode(t, fs, userID, db.UUIDString(node.ID)); got != "payload" {
		t.Fatalf("existing file overwritten: %q", got)
	}
	// Another user's tree is not reachable.
	if _, err := fs.Adopt(ctx, userID, tmp2, "00000000-0000-0000-0000-000000000000/x", size2, hash2); err == nil {
		t.Fatal("adopt outside the user's tree succeeded")
	}
}

// An in-place replacement computed from an old version must not overwrite a newer one.
func TestReplaceContentInPlaceAtRefusesStaleVersion(t *testing.T) {
	ctx := context.Background()
	fs, _, userID, _ := setupFS(t)

	first, err := fs.Push(ctx, userID, nil, "song.mp3", nil, "i", strings.NewReader("v1"))
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	id := db.UUIDString(first.Node.ID)
	base := first.Node.Version
	// A sync push lands while the tags are being edited.
	if _, err := fs.Push(ctx, userID, nil, "song.mp3", nil, "i", strings.NewReader("v2 from sync")); err != nil {
		t.Fatalf("second push: %v", err)
	}
	if _, err := fs.ReplaceContentInPlaceAt(ctx, userID, id, base, strings.NewReader("v1 with tags")); !errors.Is(err, storage.ErrStaleVersion) {
		t.Fatalf("stale replace: err = %v, want ErrStaleVersion", err)
	}
	if got := readNode(t, fs, userID, id); got != "v2 from sync" {
		t.Fatalf("content after refused replace = %q", got)
	}
	cur, err := fs.NodeByPath(ctx, userID, "/song.mp3")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fs.ReplaceContentInPlaceAt(ctx, userID, id, cur.Version, strings.NewReader("v2 with tags")); err != nil {
		t.Fatalf("current-version replace: %v", err)
	}
	if got := readNode(t, fs, userID, id); got != "v2 with tags" {
		t.Fatalf("content = %q", got)
	}
}
