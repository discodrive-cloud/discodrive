package music

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStoredLyricsRejectsSiblingSymlink(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "alice"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "bob"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "bob", "credentials"), []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../bob/credentials", filepath.Join(root, "alice", "song.lrc")); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "alice", "song.mp3")
	if raw, _ := ReadStoredLyrics(root, path); raw != "" {
		t.Fatalf("sidecar leaked: %q", raw)
	}
	if err := os.Remove(filepath.Join(root, "alice", "song.lrc")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "alice", "song.lrc"), []byte("ordinary lyrics"), 0600); err != nil {
		t.Fatal(err)
	}
	if raw, _ := ReadStoredLyrics(root, path); raw != "ordinary lyrics" {
		t.Fatalf("ordinary sidecar failed: %q", raw)
	}
}

func TestReadStoredMetaRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "target.mp3"), []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target.mp3", filepath.Join(root, "song.mp3")); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadStoredMeta(root, filepath.Join(root, "song.mp3")); err == nil {
		t.Fatal("metadata reader followed symlink")
	}
}
