package storage_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"discodrive/internal/db"
	"discodrive/internal/quota"
)

// The upload slots are few and shared by everyone. One account holding uploads open —
// several tabs on a slow link, or on purpose — must not take them all: a user gets at
// most half, the rest stay for the others.
func TestOneUserCannotHoldAllUploadSlots(t *testing.T) {
	fs, q, pool, userID, _ := setupFSPool(t, nil)
	fs.SetQuota(quota.New(q, 0))
	ctx := context.Background()
	other, err := q.CreateUser(ctx, db.CreateUserParams{
		TenantID: mustTenant(t, q, userID), Email: "other@x", PasswordHash: "x", Role: "user",
	})
	if err != nil {
		t.Fatal(err)
	}
	slots := int(pool.Config().MaxConns) - db.ListenerConns - 2
	if slots < 2 {
		t.Skipf("the pool leaves %d upload slots", slots)
	}

	// Uploads of the first user that never finish: each holds a slot.
	var stalled []*io.PipeWriter
	results := make(chan error, slots+1)
	defer func() {
		for _, w := range stalled {
			_ = w.CloseWithError(errors.New("test over"))
		}
	}()
	held := 0
	for i := 0; i < slots; i++ {
		pr, pw := io.Pipe()
		stalled = append(stalled, pw)
		name := "stalled-" + string(rune('a'+i)) + ".bin"
		go func() {
			_, err := fs.Push(ctx, userID, nil, name, nil, "dev", pr)
			results <- err
		}()
		go func() { _, _ = pw.Write([]byte("x")) }() // the upload is under way
		select {
		case err := <-results:
			if !errors.Is(err, db.ErrUploadConnectionsBusy) {
				t.Fatalf("upload %d ended with %v", i, err)
			}
			_ = pr.Close()
		case <-time.After(300 * time.Millisecond):
			held++
		}
	}
	if held >= slots {
		t.Fatalf("one user holds all %d upload slots", slots)
	}
	if held == 0 {
		t.Fatal("no upload could start")
	}
	if _, err := fs.Push(ctx, db.UUIDString(other.ID), nil, "mine.txt", nil, "dev", strings.NewReader("hello")); err != nil {
		t.Fatalf("another user's upload while the first holds %d of %d slots: %v", held, slots, err)
	}
}

func mustTenant(t *testing.T, q *db.Queries, userID string) pgtype.UUID {
	t.Helper()
	uid, _ := db.ParseUUID(userID)
	u, err := q.GetUserByID(context.Background(), uid)
	if err != nil {
		t.Fatal(err)
	}
	return u.TenantID
}
