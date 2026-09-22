package saved

import (
	"discodrive/internal/storage"
	"os"
	"path/filepath"
	"testing"
)

func TestFreeNameHandlesDirectoryAndRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "downloads", "file.txt"), 0755); err != nil {
		t.Fatal(err)
	}
	s := &Service{st: storage.NewLocalDisk(root)}
	if got, err := s.freeName("downloads", "file.txt"); err != nil || got != "downloads/file-2.txt" {
		t.Fatalf("directory collision: %s %v", got, err)
	}
	if err := os.Symlink("file.txt", filepath.Join(root, "downloads", "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.freeName("downloads", "link"); err == nil {
		t.Fatal("symlink accepted")
	}
}
