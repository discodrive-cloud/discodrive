package ebook

import (
	"bytes"
	"path/filepath"

	"discodrive/internal/storage"
)

// mimeToExt maps common image MIME types to file extensions.
var mimeToExt = map[string]string{
	"image/jpeg": "jpg",
	"image/png":  "png",
	"image/gif":  "gif",
	"image/webp": "webp",
}

// WriteCover caches cover image bytes to disk under
// <storageRoot>/.covers/ebooks/<bookID>.<ext>. The ext is derived from mimeType
// (defaults to "jpg" for unknown types). Returns the relative path from
// storageRoot (e.g. ".covers/ebooks/<id>.jpg") for storing in books.cover_path.
// The directory is created if missing. Existing files are overwritten.
func WriteCover(storageRoot, bookID string, data []byte, mimeType string) (string, error) {
	ext, ok := mimeToExt[mimeType]
	if !ok {
		ext = "jpg"
	}

	rel := filepath.Join(".covers", "ebooks", bookID+"."+ext)
	if _, _, err := storage.NewLocalDisk(storageRoot).WriteFile(rel, bytes.NewReader(data)); err != nil {
		return "", err
	}
	return rel, nil
}

// RemoveCover deletes a cached cover file at <storageRoot>/<relPath>.
// Returns nil if the file does not exist (best-effort semantics).
func RemoveCover(storageRoot, relPath string) error {
	if relPath == "" {
		return nil
	}
	return storage.NewLocalDisk(storageRoot).Remove(relPath)
}
