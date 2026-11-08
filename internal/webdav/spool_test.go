package webdav

import (
	"context"
	"errors"
	"os"
	"testing"

	"discodrive/internal/quota"
)

func newSpool(t *testing.T, ctx context.Context, user string, limit int64) *writeFile {
	t.Helper()
	tmp, err := os.CreateTemp(t.TempDir(), "spool-*")
	if err != nil {
		t.Fatal(err)
	}
	return &writeFile{ctx: ctx, userID: user, name: "f", tmp: tmp, expect: -1, limit: limit}
}

// Each PUT used to be checked only against its own snapshot of the allowance, so several
// at once could hold several times the quota in temp files. The bytes of all of a user's
// PUTs in flight share the allowance.
func TestConcurrentPutsShareTheAllowance(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	a := newSpool(t, ctx, "u1", 1000)
	b := newSpool(t, ctx, "u1", 1000)
	other := newSpool(t, ctx, "u2", 1000)

	if _, err := a.Write(make([]byte, 600)); err != nil {
		t.Fatalf("first PUT within the allowance: %v", err)
	}
	if _, err := b.Write(make([]byte, 600)); !errors.Is(err, quota.ErrExceeded) {
		t.Fatalf("second PUT past the shared allowance: err = %v, want ErrExceeded", err)
	}
	if _, err := other.Write(make([]byte, 600)); err != nil {
		t.Fatalf("another user's PUT is not affected: %v", err)
	}
	cancel()
	_ = a.Close() // aborted: its bytes leave the spool
	if _, err := b.Write(make([]byte, 600)); err != nil {
		t.Fatalf("after the first PUT ended: %v", err)
	}
	_ = b.Close()
	_ = other.Close()
}

// With no quota and no server cap the allowance is unlimited, but the temp disk is not:
// a PUT may not spool past what the disk can hold.
func TestUnlimitedPutIsBoundedByTempSpace(t *testing.T) {
	if got := spoolLimit(quota.Unlimited, 10<<30); got >= quota.Unlimited || got > 10<<30 {
		t.Fatalf("spoolLimit(unlimited, 10 GiB free) = %d, want at most the free space", got)
	}
	if got := spoolLimit(500, 10<<30); got != 500 {
		t.Fatalf("a quota below the free space stays the limit: %d", got)
	}
}
