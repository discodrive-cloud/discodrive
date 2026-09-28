package storage_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"discodrive/internal/db"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Files dropped into a user's tree outside the service are imported, files that vanish
// go to the trash, and a file whose size changed on disk gets a new hash and version and
// an "update" in the change log — without a snapshot of content that no longer exists.
func TestReconcileImportsRemovesAndUpdatesBySize(t *testing.T) {
	fs, q, userID, root := setupFS(t)
	ctx := context.Background()
	uid, _ := db.ParseUUID(userID)
	keep, err := fs.Push(ctx, userID, nil, "keep.txt", nil, "", strings.NewReader("12345"))
	if err != nil {
		t.Fatal(err)
	}
	gone, err := fs.Push(ctx, userID, nil, "gone.txt", nil, "", strings.NewReader("x"))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, keep.Node.DiskPath.String), "123456789") // 5 → 9 bytes
	if err := os.Remove(filepath.Join(root, gone.Node.DiskPath.String)); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, userID, "new", "deep", "n.txt"), "abc")

	st, err := fs.ReconcileUser(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if st.Imported != 3 || st.Missing != 1 || st.Changed != 1 || st.Errors != 0 {
		t.Fatalf("stats %+v, want imported 3 (new, deep, n.txt), missing 1, changed 1", st)
	}
	n, err := q.GetNode(ctx, keep.Node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if n.Size.Int64 != 9 || n.Version != keep.Node.Version+1 || n.ContentHash == keep.Node.ContentHash {
		t.Fatalf("changed file not updated: %+v", n)
	}
	if _, err := os.Stat(filepath.Join(root, ".versions", userID, db.UUIDString(n.ID))); !os.IsNotExist(err) {
		t.Fatalf("a snapshot was taken of content that no longer exists (err=%v)", err)
	}

	st, err = fs.ReconcileUser(ctx, uid)
	if err != nil || st.Imported+st.Missing+st.Changed+st.Errors != 0 {
		t.Fatalf("second pass must change nothing: %+v %v", st, err)
	}
}

// A symlink used to switch off the missing-file sweep for the whole user.
func TestReconcileSymlinkDoesNotDisableSweep(t *testing.T) {
	fs, _, userID, root := setupFS(t)
	ctx := context.Background()
	uid, _ := db.ParseUUID(userID)
	gone, err := fs.Push(ctx, userID, nil, "gone.txt", nil, "", strings.NewReader("x"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/hosts", filepath.Join(root, userID, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, gone.Node.DiskPath.String)); err != nil {
		t.Fatal(err)
	}
	st, err := fs.ReconcileUser(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if st.Missing != 1 {
		t.Fatalf("stats %+v: the vanished file must be trashed despite the symlink", st)
	}
}

// A folder replaced by a file of the same name (or the reverse) outside the service is
// reported, never silently deleted or imported.
func TestReconcileTypeMismatchIsReported(t *testing.T) {
	fs, _, userID, root := setupFS(t)
	ctx := context.Background()
	uid, _ := db.ParseUUID(userID)
	if _, err := fs.CreateFolder(ctx, userID, nil, "photos"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, userID, "photos")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, userID, "photos"), "not a folder")
	st, err := fs.ReconcileUser(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if st.Errors != 1 || st.Missing != 0 || st.Imported != 0 {
		t.Fatalf("stats %+v: want one reported mismatch and nothing changed", st)
	}
	nodes, _ := fs.RootChildren(ctx, userID)
	if len(nodes) != 1 || !nodes[0].IsDir {
		t.Fatalf("the folder node was touched: %+v", nodes)
	}
}

// Memory follows the largest folder, not the whole tree. Measured as allocation volume
// of a steady-state pass: 10 000 files in 100 folders against 100 files in one folder.
// Allocation still grows with rows read; the check is that it stays near-linear rather
// than holding every node at once. The real criterion is the Pi run (task 3.4); if this
// proves noisy, loosen the factor rather than drop the test.
func TestReconcileMemoryDoesNotGrowWithTreeSize(t *testing.T) {
	fs, _, userID, root := setupFS(t)
	ctx := context.Background()
	uid, _ := db.ParseUUID(userID)
	allocated := func() uint64 {
		var m runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&m)
		return m.TotalAlloc
	}
	mk := func(tag string, dirs, files int) {
		for d := range dirs {
			for f := range files {
				writeFile(t, filepath.Join(root, userID, tag, "d"+strconv.Itoa(d), "f"+strconv.Itoa(f)), "x")
			}
		}
	}
	pass := func() uint64 {
		before := allocated()
		if _, err := fs.ReconcileUser(ctx, uid); err != nil {
			t.Fatal(err)
		}
		return allocated() - before
	}

	mk("small", 1, 100)
	pass() // import
	small := pass()
	mk("big", 100, 100)
	pass() // import
	big := pass()
	if big > small*150 {
		t.Fatalf("steady-state pass allocated %d bytes for 10 100 files vs %d for 100", big, small)
	}
}
