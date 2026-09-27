package ebook

import (
	"os"
	"strings"

	pdfapi "github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/log"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

func init() {
	// Silence all pdfcpu loggers so they do not pollute test output or
	// application stderr/stdout.
	log.DisableLoggers()
	// pdfcpu otherwise creates a config directory under $HOME on first use and aborts
	// the whole process when it cannot (a service user without a writable home, a
	// read-only container root). Its defaults are all we need.
	pdfapi.DisableConfigDir()
}

// maxPDFMetaBytes caps the PDFs whose metadata is read. pdfcpu loads every stream of
// the file to get at the Info dictionary, so a 100 MB scan costs ~100 MB of heap;
// larger books keep their filename as title. A var so tests can lower it.
var maxPDFMetaBytes int64 = 32 << 20

// pdfSlot lets one PDF be parsed at a time across all scans, bounding the heap spent
// on them to about maxPDFMetaBytes.
var pdfSlot = make(chan struct{}, 1)

// parsePDF reads metadata from a PDF file via the pdfcpu Info dictionary.
// It extracts Title, Author, Subject (as a tag), and Keywords (as tags).
// Cover extraction is out of scope — CoverData is always nil.
// When the Info dict is absent or Title is empty the caller (ReadMeta) falls
// back to the filename.
func parsePDF(path string) (Meta, error) {
	f, err := os.Open(path)
	if err != nil {
		return Meta{}, err
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil {
		return Meta{}, err
	} else if fi.Size() > maxPDFMetaBytes {
		return Meta{}, nil
	}
	pdfSlot <- struct{}{}
	defer func() { <-pdfSlot }()

	conf := model.NewDefaultConfiguration()
	conf.ValidationMode = model.ValidationRelaxed // ValidationRelaxed accepts real-world PDFs with minor non-conformances (and recovers imperfect xref tables).

	info, err := pdfapi.PDFInfo(f, path, nil, false, conf)
	if err != nil {
		return Meta{}, err
	}

	var m Meta

	m.Title = strings.TrimSpace(info.Title)

	if author := strings.TrimSpace(info.Author); author != "" {
		m.Authors = []Author{{
			Name:     author,
			SortName: strings.ToLower(author),
		}}
	}

	// Collect tags from Subject and Keywords.
	if s := strings.TrimSpace(info.Subject); s != "" {
		m.Tags = append(m.Tags, s)
	}
	for _, kw := range info.Keywords {
		if kw = strings.TrimSpace(kw); kw != "" {
			m.Tags = append(m.Tags, kw)
		}
	}

	m.Date = strings.TrimSpace(info.CreationDate)

	return m, nil
}
