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
}

// parsePDF reads metadata from a PDF file via the pdfcpu Info dictionary.
// It extracts Title, Author, Subject (as a tag), and Keywords (as tags).
// Cover extraction is out of scope — CoverData is always nil.
// When the Info dict is absent or Title is empty the caller (ReadMeta) falls
// back to the filename.
// pdfcpu otherwise creates a config directory under $HOME on first use and aborts the
// whole process when it cannot (a service user without a writable home, a read-only
// container root). Its defaults are all we need.
func init() { pdfapi.DisableConfigDir() }

func parsePDF(path string) (Meta, error) {
	f, err := os.Open(path)
	if err != nil {
		return Meta{}, err
	}
	defer f.Close()

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
