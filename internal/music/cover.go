package music

import (
	"os"
	"path/filepath"

	"github.com/dhowden/tag"

	"discodrive/internal/safecontent"
	"discodrive/internal/storage"
)

// candidateCovers is the ordered list of sibling filenames checked when
// resolving album cover art. First match wins.
var candidateCovers = []string{
	"cover.jpg",
	"cover.png",
	"folder.jpg",
	"folder.png",
}

// ResolveCoverPath returns the absolute path to a sibling cover image in dir,
// and true if one is found. Returns ("", false) when no candidate exists.
func ResolveCoverPath(dir string) (string, bool) {
	for _, name := range candidateCovers {
		candidate := filepath.Join(dir, name)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, true
		}
	}
	return "", false
}

// EmbeddedCover returns the embedded cover picture of an audio file, if any.
// It reads the file tags and extracts the first attached picture.
// Returns (nil, "", false) when no picture is present or reading fails.
func EmbeddedCover(path string) (data []byte, mime string, ok bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, "", false
	}
	defer f.Close()

	m, err := tag.ReadFrom(f)
	if err != nil {
		return nil, "", false
	}

	pic := m.Picture()
	if pic == nil || len(pic.Data) == 0 {
		return nil, "", false
	}

	ct, valid := safecontent.Raster(pic.Data)
	if !valid {
		return nil, "", false
	}
	return pic.Data, ct, true
}

// EmbeddedStoredCover reads only from a securely opened storage file.
func EmbeddedStoredCover(root, path string) ([]byte, string, bool) {
	f, source, err := storage.NewLocalDisk(root).Pin(path)
	if err != nil {
		return nil, "", false
	}
	defer f.Close()
	return EmbeddedCover(source)
}
