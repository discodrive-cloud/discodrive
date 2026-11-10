package storage_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"discodrive/internal/db"
)

// A file put on the disk directly (over SMB, on the NAS) is unknown to the database
// until a rescan. An upload to the same name used to replace it without a trace; it is
// now kept next to the upload under another name, for the rescan to pick up.
func TestUploadDoesNotDestroyFileUnknownToTheDatabase(t *testing.T) {
	fs, _, userID, root := setupFS(t)
	ctx := context.Background()
	// The user's directory appears with the first upload.
	if _, err := fs.Push(ctx, userID, nil, "other.txt", nil, "dev", strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, userID, "report.pdf"), "placed on the NAS by hand")

	res, err := fs.Push(ctx, userID, nil, "report.pdf", nil, "dev", strings.NewReader("uploaded"))
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if got := readNode(t, fs, userID, db.UUIDString(res.Node.ID)); got != "uploaded" {
		t.Fatalf("uploaded content = %q", got)
	}
	entries, err := os.ReadDir(filepath.Join(root, userID))
	if err != nil {
		t.Fatal(err)
	}
	var kept string
	for _, e := range entries {
		b, _ := os.ReadFile(filepath.Join(root, userID, e.Name()))
		if string(b) == "placed on the NAS by hand" {
			kept = e.Name()
		}
	}
	if kept == "" {
		t.Fatal("the file that was on the disk is gone")
	}
	if kept == "report.pdf" || !strings.HasSuffix(kept, ".pdf") {
		t.Errorf("the disk file was kept as %q, want another .pdf name", kept)
	}
}
