// Package ebook provides e-book metadata scanning for the OPDS catalog service.
// It mirrors the structure of internal/music: a ReadMeta dispatcher, an
// IsBookFile predicate, and per-format parsers (epub.go, fb2.go, …).
package ebook

import (
	"errors"
	"path/filepath"
	"strings"
)

// bookExt returns the logical extension key for path, used to look up
// bookExtensions. For compound suffixes like ".fb2.zip" it returns "fb2.zip"
// rather than the single-level ".zip" that filepath.Ext would return.
func bookExt(path string) string {
	lower := strings.ToLower(path)
	if strings.HasSuffix(lower, ".fb2.zip") {
		return "fb2.zip"
	}
	return strings.TrimPrefix(filepath.Ext(lower), ".")
}

// bookExtensions maps lowercase file extensions (without dot) to MIME content types.
// Note: "fb2.zip" is a compound key matched by suffix check in ReadMeta/IsBookFile;
// it must appear here so those functions can look it up.
var bookExtensions = map[string]string{
	"epub":    "application/epub+zip",
	"fb2":     "application/x-fictionbook+xml",
	"fb2.zip": "application/x-fictionbook+xml",
	"pdf":     "application/pdf",
	"mobi":    "application/x-mobipocket-ebook",
	"azw":     "application/vnd.amazon.ebook",
	"azw3":    "application/vnd.amazon.ebook",
	"cbz":     "application/vnd.comicbook+zip",
	"cbr":     "application/vnd.comicbook-rar",
}

// Author holds the display name and sort name for a book author.
type Author struct {
	Name     string
	SortName string
}

// Meta holds metadata extracted from one e-book file.
type Meta struct {
	Title, SortTitle, Language, ISBN, Description, Publisher, Date string
	Series                                                         string
	SeriesIndex                                                    float64
	Authors                                                        []Author
	Tags                                                           []string
	Format, ContentType                                            string
	CoverData                                                      []byte
	CoverType                                                      string // MIME of CoverData, e.g. "image/jpeg"
}

// ReadMeta extracts metadata from an e-book file at path. Format and
// ContentType are derived from the file extension. Title falls back to the
// filename (without extension) when the parsed value is empty.
func ReadMeta(path string) (Meta, error) { return readMeta(path, path) }

func readMeta(path, source string) (Meta, error) {
	ext := bookExt(path)
	ct, ok := bookExtensions[ext]
	if !ok {
		return Meta{}, errors.New("ebook: unsupported format: " + ext)
	}

	var (
		m   Meta
		err error
	)
	switch ext {
	case "epub":
		m, err = parseEPUB(source)
	case "fb2":
		m, err = parseFB2(source)
	case "fb2.zip":
		m, err = parseFB2Zip(source)
	case "cbz":
		m, err = parseCBZ(source)
	case "cbr":
		m, err = parseCBR(source)
	case "pdf":
		m, err = parsePDF(source)
	case "mobi", "azw", "azw3":
		m, err = parseMOBI(source)
	default:
		return Meta{}, errors.New("ebook: unsupported format: " + ext)
	}
	if err != nil {
		return Meta{}, err
	}

	// Populate format/content-type fields.
	m.Format = ext
	m.ContentType = ct

	// Fall back to filename when title is missing.
	if m.Title == "" {
		m.Title = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}

	return m, nil
}

// IsBookFile reports whether the file at path has a supported e-book extension.
func IsBookFile(path string) bool {
	_, ok := bookExtensions[bookExt(path)]
	return ok
}
