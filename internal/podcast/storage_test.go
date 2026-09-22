package podcast

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStoreDownloadRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "episode.mp3")
	if err := os.WriteFile(secret, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "podcasts")); err != nil {
		t.Fatal(err)
	}
	_, _, _, err := StoreDownload(root, "podcasts/episode.mp3", func(dest string) (int64, string, string, error) {
		err := os.WriteFile(dest, []byte("download"), 0600)
		return 8, "audio/mpeg", "mp3", err
	})
	if err == nil {
		t.Fatal("download followed symlink")
	}
	if data, err := os.ReadFile(secret); err != nil || string(data) != "private" {
		t.Fatalf("secret changed: %q %v", data, err)
	}
}
