package storage_test

import (
	"context"
	"discodrive/internal/db"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRegressionUndeleteFolderEmitsDescendants(t *testing.T) {
	ctx := context.Background()
	fs, q, uid, _ := setupFS(t)
	d, err := fs.CreateFolder(ctx, uid, nil, "folder")
	if err != nil {
		t.Fatal(err)
	}
	did := db.UUIDString(d.ID)
	sub, err := fs.CreateFolder(ctx, uid, &did, "nested")
	if err != nil {
		t.Fatal(err)
	}
	sid := db.UUIDString(sub.ID)
	f, err := fs.Push(ctx, uid, &sid, "file.txt", nil, "test", strings.NewReader("content"))
	if err != nil {
		t.Fatal(err)
	}
	if err = fs.Delete(ctx, uid, did); err != nil {
		t.Fatal(err)
	}
	u, _ := db.ParseUUID(uid)
	cursor, err := q.GetUserChangeSeq(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = fs.Undelete(ctx, uid, did); err != nil {
		t.Fatal(err)
	}
	rows, err := q.ListChangesSince(ctx, db.ListChangesSinceParams{UserID: u, Seq: cursor, Lim: 100, IncludePurged: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("restore feed=%+v", rows)
	}
	for i, want := range []string{did, sid, db.UUIDString(f.Node.ID)} {
		if db.UUIDString(rows[i].NodeID) != want || rows[i].Deleted {
			t.Fatalf("restore must emit parents before children: %+v", rows)
		}
	}

}
func TestRegressionReconcileMissingFolderTrashesDescendants(t *testing.T) {
	ctx := context.Background()
	fs, q, uid, root := setupFS(t)
	d, err := fs.CreateFolder(ctx, uid, nil, "folder")
	if err != nil {
		t.Fatal(err)
	}
	did := db.UUIDString(d.ID)
	f, err := fs.Push(ctx, uid, &did, "file.txt", nil, "test", strings.NewReader("content"))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.RemoveAll(filepath.Join(root, d.DiskPath.String)); err != nil {
		t.Fatal(err)
	}
	u, _ := db.ParseUUID(uid)
	st, err := fs.ReconcileUser(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("reconcile: %+v", st)
	if n, err := q.GetNode(ctx, f.Node.ID); err == nil {
		t.Errorf("child remains live under trashed parent: %s", n.DiskPath.String)
	}
	if err = fs.Purge(ctx, uid, did); err != nil {
		t.Errorf("cannot purge externally removed folder: %v", err)
	}
}
