package podcast

import (
	"os"
	"path/filepath"

	"discodrive/internal/storage"
)

// StoreDownload keeps path-based downloaders in a private temporary directory.
// Publishing into storage always uses the symlink-safe storage boundary.
func StoreDownload(root, rel string, download func(string) (int64, string, string, error)) (int64, string, string, error) {
	dir, err := os.MkdirTemp("", "discodrive-podcast-*")
	if err != nil {
		return 0, "", "", err
	}
	defer os.RemoveAll(dir)
	dest := filepath.Join(dir, "download"+filepath.Ext(rel))
	_, ct, suffix, err := download(dest)
	if err != nil {
		return 0, "", "", err
	}
	f, err := os.Open(dest)
	if err != nil {
		return 0, "", "", err
	}
	defer f.Close()
	size, _, err := storage.NewLocalDisk(root).WriteFile(rel, f)
	return size, ct, suffix, err
}
