package ebook

import (
	"bytes"
	"errors"
	"path/filepath"

	"discodrive/internal/safecontent"
	"discodrive/internal/storage"
)

// maxCoverBytes is the largest cover cached. Covers sit in .covers/, outside the user's
// quota: without a ceiling a small archive could unpack a 64 MiB "first page" there.
const maxCoverBytes = 8 << 20

// ErrBadCover refuses bytes that are not a raster picture, or too big a one.
var ErrBadCover = errors.New("ebook: cover is not a usable picture")

// coverExt maps the sniffed type to the cached file's extension.
var coverExt = map[string]string{
	"image/jpeg": "jpg",
	"image/png":  "png",
	"image/gif":  "gif",
	"image/webp": "webp",
}

// WriteCover caches cover image bytes to disk under
// <storageRoot>/.covers/ebooks/<bookID>.<ext> and returns the path relative to
// storageRoot for books.cover_path. The type comes from the bytes themselves — what the
// book declares is not trusted — and anything that is not a JPEG, PNG, GIF or WebP of at
// most maxCoverBytes is refused with ErrBadCover: the book is then listed without a
// cover. A cover cached earlier under another extension is removed.
func WriteCover(storageRoot, bookID string, data []byte) (string, error) {
	ct, ok := safecontent.Raster(data)
	if !ok || len(data) > maxCoverBytes {
		return "", ErrBadCover
	}
	disk := storage.NewLocalDisk(storageRoot)
	rel := filepath.Join(".covers", "ebooks", bookID+"."+coverExt[ct])
	if _, _, err := disk.WriteFile(rel, bytes.NewReader(data)); err != nil {
		return "", err
	}
	for _, ext := range coverExt {
		if other := filepath.Join(".covers", "ebooks", bookID+"."+ext); other != rel {
			_ = disk.Remove(other)
		}
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
