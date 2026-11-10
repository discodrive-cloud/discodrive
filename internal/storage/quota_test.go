package storage_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/db"
	"discodrive/internal/quota"
	"discodrive/internal/storage"
)

// setQuota gives the user a personal quota of n bytes.
func setQuota(t *testing.T, q *db.Queries, userID string, n int64) {
	t.Helper()
	uid, err := db.ParseUUID(userID)
	if err != nil {
		t.Fatalf("parse uuid: %v", err)
	}
	if _, err := q.UpdateUser(context.Background(), db.UpdateUserParams{
		ID: uid, StorageQuota: pgtype.Int8{Int64: n, Valid: true}, Role: "user",
	}); err != nil {
		t.Fatalf("set quota: %v", err)
	}
}

// push uploads a file of n bytes.
func push(fs *storage.FileService, userID, name string, n int) error {
	_, err := fs.Push(context.Background(), userID, nil, name, nil, "",
		strings.NewReader(strings.Repeat("x", n)))
	return err
}

// A file that does not fit the quota must be refused, and must leave nothing behind:
// no node, and no staged bytes counted against the user.
func TestQuota_PushRefusesOverQuota(t *testing.T) {
	fs, q, userID, _ := setupFS(t)
	fs.SetQuota(quota.New(q, 0))
	setQuota(t, q, userID, 1000)

	if err := push(fs, userID, "fits.bin", 900); err != nil {
		t.Fatalf("a file inside the quota must be accepted: %v", err)
	}
	err := push(fs, userID, "too-big.bin", 200)
	if !errors.Is(err, quota.ErrExceeded) {
		t.Fatalf("want ErrExceeded, got %v", err)
	}
	nodes, err := fs.RootChildren(context.Background(), userID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, n := range nodes {
		if n.Name == "too-big.bin" {
			t.Fatal("the refused upload created a node")
		}
	}
}

// Overwriting a file does not free its old bytes — they stay in .versions — so version
// snapshots have to count against the quota. Without this a user with a 1 KB quota can
// store an unbounded amount by rewriting one file.
func TestQuota_VersionsCountAgainstQuota(t *testing.T) {
	fs, q, userID, _ := setupFS(t)
	fs.SetQuota(quota.New(q, 0))
	setQuota(t, q, userID, 1000)

	// A fresh file is the live file and nothing else: 300 used.
	if err := push(fs, userID, "f.bin", 300); err != nil {
		t.Fatalf("first write: %v", err)
	}
	// Rewriting pushes the old content into .versions: 300 live + 300 snapshot = 600.
	if err := push(fs, userID, "f.bin", 300); err != nil {
		t.Fatalf("second write (600 of a 1000 quota): %v", err)
	}
	// A third rewrite would need 300 more with 400 left — still fits, and now 900.
	if err := push(fs, userID, "f.bin", 300); err != nil {
		t.Fatalf("third write (900 of a 1000 quota): %v", err)
	}
	if err := push(fs, userID, "f.bin", 300); !errors.Is(err, quota.ErrExceeded) {
		t.Fatalf("rewriting past the quota must be refused, got %v", err)
	}
}

// Deleting a file moves it to the trash, where it sits on disk for TRASH_DAYS. Until
// it is really gone it still costs quota — otherwise the trash is a free parking lot.
func TestQuota_TrashedFilesStillCount(t *testing.T) {
	fs, q, userID, _ := setupFS(t)
	fs.SetQuota(quota.New(q, 0))
	setQuota(t, q, userID, 1000)

	res, err := fs.Push(context.Background(), userID, nil, "f.bin", nil, "",
		strings.NewReader(strings.Repeat("x", 900)))
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := fs.Delete(context.Background(), userID, db.UUIDString(res.Node.ID)); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := push(fs, userID, "g.bin", 900); !errors.Is(err, quota.ErrExceeded) {
		t.Fatalf("trashed bytes must still count, got %v", err)
	}
	// Emptying the trash releases them for real.
	if err := fs.PurgeAll(context.Background(), userID); err != nil {
		t.Fatalf("purge: %v", err)
	}
	if err := push(fs, userID, "g.bin", 900); err != nil {
		t.Fatalf("after emptying the trash the space must be free again: %v", err)
	}
}

// A user with no personal quota is still bounded by the server-wide cap: that is what
// keeps discodrive inside its slice of a shared disk.
func TestQuota_ServerCapBoundsUnlimitedUsers(t *testing.T) {
	fs, q, userID, _ := setupFS(t)
	fs.SetQuota(quota.New(q, 1000)) // no user quota set — only the cap applies

	if err := push(fs, userID, "a.bin", 900); err != nil {
		t.Fatalf("write inside the cap: %v", err)
	}
	if err := push(fs, userID, "b.bin", 200); !errors.Is(err, quota.ErrExceeded) {
		t.Fatalf("the server cap must stop the write, got %v", err)
	}
}

// A stream that lies about its length (or declares none) must die at the limit rather
// than after the whole body has landed in staging.
func TestQuota_StreamStopsAtTheLimit(t *testing.T) {
	fs, q, userID, _ := setupFS(t)
	fs.SetQuota(quota.New(q, 0))
	setQuota(t, q, userID, 500)

	if err := push(fs, userID, "big.bin", 5_000_000); !errors.Is(err, quota.ErrExceeded) {
		t.Fatalf("want ErrExceeded, got %v", err)
	}
}

// A resumable upload declares its size up front: refuse it at init, before the client
// spends bandwidth on chunks that cannot be committed.
func TestQuota_UploadInitRefusesDeclaredSize(t *testing.T) {
	fs, q, userID, root := setupFS(t)
	checker := quota.New(q, 0)
	fs.SetQuota(checker)
	setQuota(t, q, userID, 1000)

	u := storage.NewUploads(storage.NewLocalDisk(root), fs)
	u.SetQuota(checker)

	if _, err := u.Init(context.Background(), userID, nil, "ok.bin", 900, storage.PushMeta{}); err != nil {
		t.Fatalf("init inside the quota: %v", err)
	}
	_, err := u.Init(context.Background(), userID, nil, "big.bin", 2000, storage.PushMeta{})
	if !errors.Is(err, quota.ErrExceeded) {
		t.Fatalf("want ErrExceeded at init, got %v", err)
	}
}

// Chunks are the path an upload with no declared size takes; the per-chunk limiter is
// the only thing standing between it and a full disk.
func TestQuota_ChunksStopAtTheLimit(t *testing.T) {
	fs, q, userID, root := setupFS(t)
	checker := quota.New(q, 0)
	fs.SetQuota(checker)
	setQuota(t, q, userID, 1000)

	u := storage.NewUploads(storage.NewLocalDisk(root), fs)
	u.SetQuota(checker)

	id, err := u.Init(context.Background(), userID, nil, "undeclared.bin", 0, storage.PushMeta{})
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	chunk := strings.Repeat("x", 400)
	for i := range 2 {
		if _, err := u.Chunk(context.Background(), id, userID, i, strings.NewReader(chunk)); err != nil {
			t.Fatalf("chunk %d: %v", i, err)
		}
	}
	// 800 bytes staged against a 1000-byte quota: a third 400-byte chunk does not fit.
	if _, err := u.Chunk(context.Background(), id, userID, 2, strings.NewReader(chunk)); !errors.Is(err, quota.ErrExceeded) {
		t.Fatalf("want ErrExceeded on the chunk crossing the quota, got %v", err)
	}
}

// One account cannot hold an unbounded number of unfinished uploads open: each costs
// memory and a reservation row until it completes or is collected.
func TestUploads_OpenSessionsPerUserAreBounded(t *testing.T) {
	fs, _, userID, root := setupFS(t)
	u := storage.NewUploads(storage.NewLocalDisk(root), fs)
	ctx := context.Background()
	var last string
	for i := 0; ; i++ {
		id, err := u.Init(ctx, userID, nil, fmt.Sprintf("f%d.bin", i), 10, storage.PushMeta{})
		if errors.Is(err, storage.ErrTooManyUploads) {
			if i < 100 {
				t.Fatalf("refused after only %d open uploads", i)
			}
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		last = id
		if i > 5000 {
			t.Fatal("5000 uploads were opened without a refusal")
		}
	}
	// Finishing or cancelling one makes room again.
	u.Abort(userID, last)
	if _, err := u.Init(ctx, userID, nil, "after.bin", 10, storage.PushMeta{}); err != nil {
		t.Fatalf("after cancelling one upload: %v", err)
	}
}
