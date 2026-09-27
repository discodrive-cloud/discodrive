package quota_test

import (
	"errors"
	"io"
	"testing"

	"discodrive/internal/quota"
)

// chunky yields n zero bytes at most 32 KiB per Read, like a request body.
type chunky struct{ left int64 }

func (c *chunky) Read(p []byte) (int, error) {
	if c.left == 0 {
		return 0, io.EOF
	}
	n := int64(min(len(p), 32<<10))
	n = min(n, c.left)
	clear(p[:n])
	c.left -= n
	return int(n), nil
}

// Accounting ran a locked transaction for every Read (~32 KiB): tens of thousands per
// gigabyte, all uploads serialized behind one lock. It now reserves in blocks.
func TestReservationReaderReservesInBlocks(t *testing.T) {
	q, ctx := setup(t)
	mb := int64(1) << 20
	limit := 100 * mb
	user := makeUser(t, q, ctx, "blocks@test.local", &limit)
	c := quota.New(q, 0)
	if err := c.CreateReservation(ctx, "blocks", user); err != nil {
		t.Fatal(err)
	}
	res, err := c.LockReservation(ctx, "blocks")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Close()

	r := res.Reader(&chunky{left: 10 * mb})
	if _, err := io.CopyN(io.Discard, r, 100<<10); err != nil {
		t.Fatal(err)
	}
	row, err := q.GetUploadReservation(ctx, "blocks")
	if err != nil {
		t.Fatal(err)
	}
	if row.Bytes < quota.ReserveBlock {
		t.Fatalf("after 100 KiB the reservation is %d bytes; want a whole block (%d) reserved ahead", row.Bytes, quota.ReserveBlock)
	}
	n, err := io.Copy(io.Discard, r)
	if err != nil {
		t.Fatal(err)
	}
	if got := 100<<10 + n; got != 10*mb {
		t.Fatalf("read %d bytes, want %d", got, 10*mb)
	}
	row, _ = q.GetUploadReservation(ctx, "blocks")
	if row.Bytes < 10*mb || row.Bytes > 10*mb+quota.ReserveBlock {
		t.Fatalf("reservation %d bytes after reading %d: must cover the data and overshoot by less than a block", row.Bytes, 10*mb)
	}
}

// Reserving ahead must never let a read past the quota: the block shrinks to what is left.
func TestReservationReaderStopsAtQuota(t *testing.T) {
	q, ctx := setup(t)
	mb := int64(1) << 20
	limit := 5*mb + 12345
	user := makeUser(t, q, ctx, "edge@test.local", &limit)
	c := quota.New(q, 0)
	if err := c.CreateReservation(ctx, "edge", user); err != nil {
		t.Fatal(err)
	}
	res, err := c.LockReservation(ctx, "edge")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Close()

	n, err := io.Copy(io.Discard, res.Reader(&chunky{left: 6 * mb}))
	if !errors.Is(err, quota.ErrExceeded) {
		t.Fatalf("err = %v, want ErrExceeded", err)
	}
	if n > limit {
		t.Fatalf("passed %d bytes through, quota is %d", n, limit)
	}
	row, _ := q.GetUploadReservation(ctx, "edge")
	if row.Bytes > limit {
		t.Fatalf("reserved %d bytes, quota is %d", row.Bytes, limit)
	}
}

// Near the edge of the quota the block shrinks, so a parallel upload of the same user
// still finds room.
func TestReservationReaderLeavesRoomNearQuota(t *testing.T) {
	q, ctx := setup(t)
	limit := int64(1) << 20
	user := makeUser(t, q, ctx, "near@test.local", &limit)
	c := quota.New(q, 0)
	if err := c.CreateReservation(ctx, "near", user); err != nil {
		t.Fatal(err)
	}
	res, err := c.LockReservation(ctx, "near")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Close()

	if _, err := io.CopyN(io.Discard, res.Reader(&chunky{left: limit}), 32<<10); err != nil {
		t.Fatal(err)
	}
	row, _ := q.GetUploadReservation(ctx, "near")
	if row.Bytes > limit/2+32<<10 {
		t.Fatalf("reserved %d of a %d quota after reading 32 KiB: more than half taken ahead", row.Bytes, limit)
	}
}
