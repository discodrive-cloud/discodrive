package webdav_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

// A PUT body goes straight into storage staging — the disk the file will live on, under
// the quota. It used to be buffered whole in the temp directory first: every file was
// written twice, and a file larger than the free space of the system disk was refused
// although the data disk had room. With an unusable TMPDIR any such buffering fails.
func TestPutIsNotBufferedInTheTempDirectory(t *testing.T) {
	ctx := context.Background()
	wfs, _, _ := setup(t)
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))

	body := bytes.Repeat([]byte("0123456789abcdef"), 1<<16) // 1 MiB
	wf, err := wfs.OpenFile(ctx, "/big.bin", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		t.Fatalf("open for writing: %v", err)
	}
	for off := 0; off < len(body); off += 32 << 10 {
		if _, err := wf.Write(body[off : off+32<<10]); err != nil {
			t.Fatalf("write at %d: %v", off, err)
		}
	}
	if err := wf.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if got := readBack(t, wfs, "/big.bin"); !bytes.Equal(got, body) {
		t.Fatalf("stored %d bytes, differing from the %d sent", len(got), len(body))
	}
}
