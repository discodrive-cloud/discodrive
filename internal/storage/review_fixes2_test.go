package storage_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"discodrive/internal/db"
	"discodrive/internal/storage"
)

func sha(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// Two devices pushing the same path at once must both be accepted (the later one as an
// overwrite), and every byte on disk must match the row that describes it: the live
// file its node, each snapshot its file_versions row.
func TestConcurrentPushSamePathKeepsBytesAndRowsInStep(t *testing.T) {
	ctx := context.Background()
	fs, q, userID, root := setupFS(t)

	const writers = 8
	for round := 0; round < 3; round++ {
		name := fmt.Sprintf("same-%d.txt", round)
		var wg sync.WaitGroup
		errs := make([]error, writers)
		start := make(chan struct{})
		for i := 0; i < writers; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				body := strings.Repeat(fmt.Sprintf("writer %d round %d\n", i, round), 2000)
				_, errs[i] = fs.Push(ctx, userID, nil, name, nil, "d", strings.NewReader(body))
			}(i)
		}
		close(start)
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Fatalf("round %d writer %d: %v", round, i, err)
			}
		}

		node, err := fs.NodeByPath(ctx, userID, "/"+name)
		if err != nil {
			t.Fatalf("node: %v", err)
		}
		if node.Version != writers {
			t.Fatalf("round %d: version %d, want %d (every push applied once)", round, node.Version, writers)
		}
		live, err := os.ReadFile(filepath.Join(root, node.DiskPath.String))
		if err != nil {
			t.Fatal(err)
		}
		if sha(live) != node.ContentHash.String {
			t.Fatalf("round %d: live bytes do not match the node's hash", round)
		}
		versions, err := q.ListFileVersions(ctx, node.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(versions) != writers-1 {
			t.Fatalf("round %d: %d snapshots, want %d", round, len(versions), writers-1)
		}
		seen := map[string]bool{sha(live): true}
		for _, v := range versions {
			b, err := os.ReadFile(filepath.Join(root, v.DiskPath.String))
			if err != nil {
				t.Fatal(err)
			}
			if sha(b) != v.ContentHash.String {
				t.Fatalf("round %d: snapshot of v%d holds other content than its row says", round, v.Version)
			}
			seen[v.ContentHash.String] = true
		}
		if len(seen) != writers {
			t.Fatalf("round %d: %d distinct contents kept, want %d (a push was lost)", round, len(seen), writers)
		}
	}
}

// A device name is client input: it must not add path segments to the conflict copy.
func TestConflictCopyNameSanitizesDevice(t *testing.T) {
	ctx := context.Background()
	fs, _, userID, root := setupFS(t)

	dir, err := fs.CreateFolder(ctx, userID, nil, "notes")
	if err != nil {
		t.Fatal(err)
	}
	did := db.UUIDString(dir.ID)
	first, err := fs.Push(ctx, userID, &did, "note.txt", nil, "a", strings.NewReader("v1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Push(ctx, userID, &did, "note.txt", i64(first.Node.Version), "a", strings.NewReader("v2")); err != nil {
		t.Fatal(err)
	}
	for _, device := range []string{"x/y", "../../evil", "a\\b", "..", strings.Repeat("d", 300), "\x00", "ok-device_1"} {
		res, err := fs.Push(ctx, userID, &did, "note.txt", i64(first.Node.Version), device, strings.NewReader("mine "+device))
		if err != nil {
			t.Fatalf("device %q: %v", device, err)
		}
		if !res.Conflicted {
			t.Fatalf("device %q: expected a conflict copy", device)
		}
		n := res.Node
		if strings.ContainsAny(n.Name, "/\\\x00") || len(n.Name) > 255 {
			t.Fatalf("device %q: unsafe conflict name %q", device, n.Name)
		}
		if n.ParentID != dir.ID {
			t.Fatalf("device %q: conflict copy left its folder", device)
		}
		if want := dir.DiskPath.String + "/" + n.Name; n.DiskPath.String != want {
			t.Fatalf("device %q: disk path %q, want %q", device, n.DiskPath.String, want)
		}
		if _, err := os.Stat(filepath.Join(root, n.DiskPath.String)); err != nil {
			t.Fatalf("device %q: conflict copy not on disk: %v", device, err)
		}
	}
	if device := "ok-device_1"; !strings.Contains(conflictNames(t, fs, userID, did), device) {
		t.Fatalf("a harmless device name should be kept as is")
	}
	// Nothing but the folder's own files: no directories were created by a device name.
	entries, err := os.ReadDir(filepath.Join(root, dir.DiskPath.String))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() {
			t.Fatalf("a device name created the folder %q", e.Name())
		}
	}
}

func conflictNames(t *testing.T, fs *storage.FileService, userID, dirID string) string {
	t.Helper()
	kids, err := fs.ListChildren(context.Background(), userID, dirID)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, k := range kids {
		names = append(names, k.Name)
	}
	return strings.Join(names, "\n")
}
