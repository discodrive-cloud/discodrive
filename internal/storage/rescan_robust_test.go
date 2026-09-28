package storage_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"discodrive/internal/db"
)

// Concurrent Rescan calls (startup, the admin panel and the rescan command) must not
// step on each other: no call may fail with a name conflict, and every file
// still gets exactly one live node.
func TestRescan_ConcurrentCallsSafe(t *testing.T) {
	ctx := context.Background()
	fs, q, userID, root := setupFS(t)
	uid, _ := db.ParseUUID(userID)

	userDir := filepath.Join(root, userID)
	if err := os.MkdirAll(userDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	const n = 30
	for i := range n {
		name := fmt.Sprintf("f%02d.txt", i)
		if err := os.WriteFile(filepath.Join(userDir, name), []byte(name), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	const workers = 4
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for g := range workers {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			errs[g] = fs.Rescan(ctx)
		}(g)
	}
	wg.Wait()

	for g, err := range errs {
		if err != nil {
			t.Errorf("concurrent Rescan #%d: %v", g, err)
		}
	}
	for i := range n {
		rel := fmt.Sprintf("%s/f%02d.txt", userID, i)
		if _, err := q.GetLiveNodeByPath(ctx, db.GetLiveNodeByPathParams{UserID: uid, Path: rel}); err != nil {
			t.Errorf("file %s has no live node after concurrent rescans: %v", rel, err)
		}
	}
}

// One unreadable directory must not abort the whole rescan: new files elsewhere
// (including other users) are still imported, and nodes under the unreadable
// directory are NOT soft-deleted as "missing".
func TestRescan_ContinuesPastUnreadableDir(t *testing.T) {
	ctx := context.Background()
	fs, q, userA, root := setupFS(t)
	uidA, _ := db.ParseUUID(userA)

	// user A: an indexed folder "aaa" with a file inside, then make it unreadable.
	folder, err := fs.CreateFolder(ctx, userA, nil, "aaa")
	if err != nil {
		t.Fatalf("create folder: %v", err)
	}
	folderID := db.UUIDString(folder.ID)
	if _, err := fs.Push(ctx, userA, &folderID, "inside.txt", nil, "", strings.NewReader("hi")); err != nil {
		t.Fatalf("push: %v", err)
	}
	aaaDir := filepath.Join(root, userA, "aaa")
	if err := os.Chmod(aaaDir, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(aaaDir, 0o755) })

	// user A: a new file dropped manually at the root.
	if err := os.WriteFile(filepath.Join(root, userA, "zzz.txt"), []byte("new"), 0o644); err != nil {
		t.Fatalf("write zzz: %v", err)
	}

	// user B (separate tenant): a new file dropped manually.
	tenantB, err := q.CreateTenant(ctx, "t2")
	if err != nil {
		t.Fatalf("tenant b: %v", err)
	}
	uB, err := q.CreateUser(ctx, db.CreateUserParams{
		TenantID: tenantB.ID, Email: "b@x", PasswordHash: "x", Role: "user",
	})
	if err != nil {
		t.Fatalf("user b: %v", err)
	}
	userB := db.UUIDString(uB.ID)
	if err := os.MkdirAll(filepath.Join(root, userB), 0o755); err != nil {
		t.Fatalf("mkdir b: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, userB, "b.txt"), []byte("b"), 0o644); err != nil {
		t.Fatalf("write b: %v", err)
	}

	// The error (if any) is logged/aggregated, but must not block the rest.
	_ = fs.Rescan(ctx)

	if _, err := q.GetLiveNodeByPath(ctx, db.GetLiveNodeByPathParams{UserID: uidA, Path: userA + "/zzz.txt"}); err != nil {
		t.Errorf("user A: zzz.txt not imported despite unreadable sibling dir: %v", err)
	}
	if _, err := q.GetLiveNodeByPath(ctx, db.GetLiveNodeByPathParams{UserID: uB.ID, Path: userB + "/b.txt"}); err != nil {
		t.Errorf("user B: b.txt not imported (rescan aborted on user A?): %v", err)
	}
	// The file inside the unreadable dir must stay alive: an incomplete walk is
	// no proof of deletion.
	if _, err := q.GetLiveNodeByPath(ctx, db.GetLiveNodeByPathParams{UserID: uidA, Path: userA + "/aaa/inside.txt"}); err != nil {
		t.Errorf("inside.txt is not alive after a partial walk (soft-deleted?): %v", err)
	}
}
