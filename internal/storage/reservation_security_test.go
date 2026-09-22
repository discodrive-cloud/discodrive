package storage_test

import (
	"context"
	"discodrive/internal/db"
	"discodrive/internal/quota"
	"discodrive/internal/storage"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestUploadReservationAggregateAndCompletion(t *testing.T) {
	fs, q, user, root := setupFS(t)
	ctx := context.Background()
	setQuota(t, q, user, 1000)
	checker := quota.New(q, 1000)
	fs.SetQuota(checker)
	disk := storage.NewLocalDisk(root)
	uploads := storage.NewUploads(disk, fs)
	if err := uploads.SetQuota(checker); err != nil {
		t.Fatal(err)
	}
	first, err := uploads.Init(ctx, user, nil, "first", 1000, storage.PushMeta{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := uploads.Init(ctx, user, nil, "second", 0, storage.PushMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := uploads.Chunk(ctx, first, user, 0, strings.NewReader(strings.Repeat("x", 900))); err != nil {
		t.Fatal(err)
	}
	if _, err := uploads.Chunk(ctx, second, user, 0, strings.NewReader(strings.Repeat("x", 900))); !errors.Is(err, quota.ErrExceeded) {
		t.Fatalf("aggregate bypass: %v", err)
	}
	if n, err := checker.Used(ctx); err != nil || n != 900 {
		t.Fatalf("accounted=%d err=%v", n, err)
	}
	if _, err := uploads.Chunk(ctx, first, user, 1, strings.NewReader(strings.Repeat("x", 100))); err != nil {
		t.Fatal(err)
	}
	result, err := uploads.Complete(ctx, first, user)
	if err != nil {
		t.Fatalf("full quota completion: %v", err)
	}
	if result.Node.Size.Int64 != 1000 {
		t.Fatal("size mismatch")
	}
	if n, err := checker.Used(ctx); err != nil || n != 1000 {
		t.Fatalf("double charging: %d %v", n, err)
	}
	if n, err := q.TotalUploadReserved(ctx); err != nil || n != 0 {
		t.Fatalf("reservation leaked: %d %v", n, err)
	}
	uploads.Abort(user, second)
}

func TestUploadReservationAcrossInstancesAndRecovery(t *testing.T) {
	fs, q, user, root := setupFS(t)
	ctx := context.Background()
	setQuota(t, q, user, 1000)
	checker := quota.New(q, 1000)
	fs.SetQuota(checker)
	disk := storage.NewLocalDisk(root)
	a := storage.NewUploads(disk, fs)
	b := storage.NewUploads(disk, fs)
	if err := a.SetQuota(checker); err != nil {
		t.Fatal(err)
	}
	if err := b.SetQuota(quota.New(q, 1000)); err != nil {
		t.Fatal(err)
	}
	id1, err := a.Init(ctx, user, nil, "one", 0, storage.PushMeta{})
	if err != nil {
		t.Fatal(err)
	}
	id2, err := b.Init(ctx, user, nil, "two", 0, storage.PushMeta{})
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			u, id := a, id1
			if i == 1 {
				u, id = b, id2
			}
			_, err := u.Chunk(ctx, id, user, 0, strings.NewReader(strings.Repeat("x", 900)))
			results <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	wins := 0
	for err := range results {
		if err == nil {
			wins++
		} else if !errors.Is(err, quota.ErrExceeded) {
			t.Fatal(err)
		}
	}
	if wins != 1 {
		t.Fatalf("quota winners=%d", wins)
	}
	restarted := storage.NewUploads(disk, fs)
	if err := restarted.SetQuota(checker); err != nil {
		t.Fatal(err)
	}
	if n, err := checker.Used(ctx); err != nil || n != 900 {
		t.Fatalf("restart forgot accounting: %d %v", n, err)
	}
	restarted.GC(-time.Second)
	if n, err := checker.Used(ctx); err != nil || n != 0 {
		t.Fatalf("orphan GC failed: %d %v", n, err)
	}
	for _, id := range []string{id1, id2} {
		if n, _ := disk.Size(".uploads/" + id); n != 0 {
			t.Fatalf("orphan bytes remain=%d", n)
		}
	}
}

func TestUploadReservationRollbackAndAbort(t *testing.T) {
	fs, q, user, root := setupFS(t)
	ctx := context.Background()
	setQuota(t, q, user, 1000)
	checker := quota.New(q, 1000)
	disk := storage.NewLocalDisk(root)
	u := storage.NewUploads(disk, fs)
	if err := u.SetQuota(checker); err != nil {
		t.Fatal(err)
	}
	id, err := u.Init(ctx, user, nil, "retry", 0, storage.PushMeta{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = u.Chunk(ctx, id, user, 0, &failingReader{data: []byte(strings.Repeat("x", 900)), cut: 500})
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("failure: %v", err)
	}
	if n, _ := checker.Used(ctx); n != 0 {
		t.Fatalf("failed bytes charged=%d", n)
	}
	if _, err := u.Chunk(ctx, id, user, 0, strings.NewReader(strings.Repeat("x", 900))); err != nil {
		t.Fatal(err)
	}
	u.Abort(user, id)
	if n, _ := checker.Used(ctx); n != 0 {
		t.Fatalf("abort charge=%d", n)
	}
}

func TestUploadReservationGlobalCapAcrossUsers(t *testing.T) {
	fs, q, user, root := setupFS(t)
	ctx := context.Background()
	checker := quota.New(q, 1000)
	disk := storage.NewLocalDisk(root)
	u := storage.NewUploads(disk, fs)
	if err := u.SetQuota(checker); err != nil {
		t.Fatal(err)
	}
	uid, _ := db.ParseUUID(user)
	owner, err := q.GetUserByID(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	other, err := q.CreateUser(ctx, db.CreateUserParams{TenantID: owner.TenantID, Email: "other@test", PasswordHash: "test", Role: "user"})
	if err != nil {
		t.Fatal(err)
	}
	id, err := u.Init(ctx, user, nil, "one", 0, storage.PushMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := u.Chunk(ctx, id, user, 0, strings.NewReader(strings.Repeat("x", 900))); err != nil {
		t.Fatal(err)
	}
	second, err := u.Init(ctx, db.UUIDString(other.ID), nil, "two", 0, storage.PushMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := u.Chunk(ctx, second, db.UUIDString(other.ID), 0, strings.NewReader(strings.Repeat("x", 900))); !errors.Is(err, quota.ErrExceeded) {
		t.Fatalf("cross-user cap: %v", err)
	}
}

func TestUploadReservationDirectWritesShareBudget(t *testing.T) {
	fs, q, user, _ := setupFS(t)
	ctx := context.Background()
	setQuota(t, q, user, 1000)
	fs.SetQuota(quota.New(q, 1000))
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, name := range []string{"a", "b"} {
		go func(name string) {
			<-start
			_, err := fs.Push(ctx, user, nil, name, nil, "", strings.NewReader(strings.Repeat("x", 900)))
			results <- err
		}(name)
	}
	close(start)
	wins := 0
	for i := 0; i < 2; i++ {
		err := <-results
		if err == nil {
			wins++
		} else if !errors.Is(err, quota.ErrExceeded) {
			t.Fatal(err)
		}
	}
	if wins != 1 {
		t.Fatalf("direct write winners=%d", wins)
	}
	if used, err := q.TotalStorageUsage(ctx); err != nil || used != 900 {
		t.Fatalf("usage=%d %v", used, err)
	}
}

func TestUploadReservationGCDoesNotDeleteActiveChunk(t *testing.T) {
	fs, q, user, root := setupFS(t)
	ctx := context.Background()
	checker := quota.New(q, 1000)
	disk := storage.NewLocalDisk(root)
	u := storage.NewUploads(disk, fs)
	if err := u.SetQuota(checker); err != nil {
		t.Fatal(err)
	}
	other := storage.NewUploads(disk, fs)
	if err := other.SetQuota(checker); err != nil {
		t.Fatal(err)
	}
	id, err := u.Init(ctx, user, nil, "slow", 0, storage.PushMeta{})
	if err != nil {
		t.Fatal(err)
	}
	slow := newBlockingReader()
	result := make(chan error, 1)
	go func() { _, err := u.Chunk(ctx, id, user, 0, slow); result <- err }()
	select {
	case <-slow.started:
	case <-time.After(3 * time.Second):
		t.Fatal("chunk did not start")
	}
	done := make(chan struct{})
	go func() { other.GC(-time.Second); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		close(slow.release)
		t.Fatal("GC blocked on active writer")
	}
	if _, err := q.GetUploadReservation(ctx, id); err != nil {
		close(slow.release)
		t.Fatal("GC deleted active reservation")
	}
	close(slow.release)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	u.Abort(user, id)
}

func TestUploadReservationRemovesLegacyOrphansOnStartup(t *testing.T) {
	fs, q, _, root := setupFS(t)
	disk := storage.NewLocalDisk(root)
	name := ".uploads/0123456789abcdef0123456789abcdef"
	if _, _, err := disk.WriteFile(name, strings.NewReader("abandoned")); err != nil {
		t.Fatal(err)
	}
	restarted := storage.NewUploads(disk, fs)
	if err := restarted.SetQuota(quota.New(q, 1000)); err != nil {
		t.Fatal(err)
	}
	if exists, err := disk.Exists(name); err != nil || exists {
		t.Fatalf("legacy staging survived: %v %v", exists, err)
	}
}

type cancellingUploadReader struct {
	cancel context.CancelFunc
	sent   bool
}

func (r *cancellingUploadReader) Read(p []byte) (int, error) {
	if r.sent {
		r.cancel()
		return 0, context.Canceled
	}
	r.sent = true
	return copy(p, strings.Repeat("x", 500)), nil
}
func TestUploadReservationCancelledRequestReleasesBytes(t *testing.T) {
	fs, q, user, root := setupFS(t)
	checker := quota.New(q, 1000)
	disk := storage.NewLocalDisk(root)
	u := storage.NewUploads(disk, fs)
	if err := u.SetQuota(checker); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	id, err := u.Init(ctx, user, nil, "cancelled", 0, storage.PushMeta{})
	if err != nil {
		t.Fatal(err)
	}
	requestCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if _, err := u.Chunk(requestCtx, id, user, 0, &cancellingUploadReader{cancel: cancel}); !errors.Is(err, context.Canceled) {
		t.Fatalf("request error: %v", err)
	}
	if n, err := checker.Used(ctx); err != nil || n != 0 {
		t.Fatalf("cancelled request charged=%d %v", n, err)
	}
	if _, err := u.Chunk(ctx, id, user, 0, strings.NewReader(strings.Repeat("x", 900))); err != nil {
		t.Fatalf("retry: %v", err)
	}
	u.Abort(user, id)
}
