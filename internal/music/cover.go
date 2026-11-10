package music

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"discodrive/internal/music/tagwrite"
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

	m, err := tagwrite.ReadFrom(f)
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

// CoverUnchanged stamps the response for cover art cut out of an audio file with an ETag
// derived from the file's content hash, and reports whether the client already holds
// that cover (304 has then been written). It is called before the file is opened: a grid
// of albums otherwise re-parses every audio file's tags on every visit. The response
// stays private and is revalidated each time — access to the file may be revoked.
func CoverUnchanged(w http.ResponseWriter, r *http.Request, contentHash string) bool {
	if contentHash == "" {
		return false
	}
	etag := `"` + contentHash + `-cover"`
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "private, no-cache")
	for _, have := range strings.Split(r.Header.Get("If-None-Match"), ",") {
		if strings.TrimPrefix(strings.TrimSpace(have), "W/") == etag {
			w.WriteHeader(http.StatusNotModified)
			return true
		}
	}
	return false
}
