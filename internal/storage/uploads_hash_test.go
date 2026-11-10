package storage_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"discodrive/internal/db"
	"discodrive/internal/quota"
	"discodrive/internal/storage"
)

// stagingReads counts how often an assembled upload is opened for reading.
type stagingReads struct {
	storage.Storage
	opens atomic.Int64
}

func (d *stagingReads) Open(rel string) (*os.File, error) {
	if strings.HasPrefix(rel, ".uploads/") {
		d.opens.Add(1)
	}
	return d.Storage.Open(rel)
}

// failAfter yields n bytes of s and then fails, like a connection dropped mid-chunk.
type failAfter struct {
	r io.Reader
}

func (f *failAfter) Read(p []byte) (int, error) {
	n, err := f.r.Read(p)
	if err == io.EOF {
		return n, errors.New("connection reset")
	}
	return n, err
}

// The hash of a chunked upload is accumulated as the chunks arrive. Completing used to
// read the whole assembled file again just to hash it — minutes for a large file on a
// slow disk, past the proxy's timeout, with the client seeing 504 for an upload that
// then succeeded.
func TestCompleteDoesNotRereadTheAssembledFile(t *testing.T) {
	var disk *stagingReads
	fs, q, userID, _ := setupFSWith(t, func(st storage.Storage) storage.Storage {
		disk = &stagingReads{Storage: st}
		return disk
	})
	checker := quota.New(q, 0)
	fs.SetQuota(checker)
	u := storage.NewUploads(disk, fs)
	if err := u.SetQuota(checker); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	parts := []string{strings.Repeat("a", 70000), strings.Repeat("b", 70000), "tail"}
	whole := strings.Join(parts, "")
	id, err := u.Init(ctx, userID, nil, "big.bin", int64(len(whole)), storage.PushMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := u.Chunk(ctx, id, userID, 0, strings.NewReader(parts[0])); err != nil {
		t.Fatalf("chunk 0: %v", err)
	}
	// Chunk 1 dies halfway and is sent again: the half must not stay in the hash.
	if _, err := u.Chunk(ctx, id, userID, 1, &failAfter{r: strings.NewReader(parts[1][:30000])}); err == nil {
		t.Fatal("the broken chunk was accepted")
	}
	for i := 1; i < len(parts); i++ {
		if _, err := u.Chunk(ctx, id, userID, i, strings.NewReader(parts[i])); err != nil {
			t.Fatalf("chunk %d: %v", i, err)
		}
	}
	before := disk.opens.Load()
	res, err := u.Complete(ctx, id, userID)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if n := disk.opens.Load() - before; n != 0 {
		t.Errorf("Complete opened the assembled file %d time(s) to read it again", n)
	}
	sum := sha256.Sum256([]byte(whole))
	if got := res.Node.ContentHash.String; got != hex.EncodeToString(sum[:]) {
		t.Errorf("content hash = %s, want the hash of what was sent", got)
	}
	if got := readNode(t, fs, userID, uuidOf(res)); got != whole {
		t.Errorf("stored content differs from what was sent (%d bytes, want %d)", len(got), len(whole))
	}
}

func uuidOf(res storage.PushResult) string { return db.UUIDString(res.Node.ID) }
