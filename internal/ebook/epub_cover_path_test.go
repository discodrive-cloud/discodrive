package ebook

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

// The cover's href is a URL reference relative to the OPF file: percent-encoded, and
// free to climb out of the OPF's folder. Books laid out that way came out without a cover.
func TestEPUBCoverHrefIsResolvedAsAReference(t *testing.T) {
	for name, c := range map[string]struct{ href, entry string }{
		"a sibling folder":  {"../Images/cover.jpg", "Images/cover.jpg"},
		"an encoded space":  {"cover%20art.jpg", "OEBPS/cover art.jpg"},
		"a plain file name": {"cover.jpg", "OEBPS/cover.jpg"},
	} {
		p := filepath.Join(t.TempDir(), "book.epub")
		f, err := os.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		zw := zip.NewWriter(f)
		add := func(entry, body string) {
			w, err := zw.Create(entry)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write([]byte(body))
		}
		add("mimetype", "application/epub+zip")
		add("META-INF/container.xml", `<?xml version="1.0"?><container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles><rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`)
		add("OEBPS/content.opf", `<?xml version="1.0"?><package xmlns="http://www.idpf.org/2007/opf" version="3.0"><metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>T</dc:title><meta name="cover" content="cov"/></metadata><manifest><item id="cov" href="`+c.href+`" media-type="image/jpeg"/></manifest></package>`)
		add(c.entry, "JPEGDATA")
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		f.Close()
		m, err := ReadMeta(p)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if string(m.CoverData) != "JPEGDATA" {
			t.Errorf("%s: the cover at %q (href %q) was not found", name, c.entry, c.href)
		}
	}
}
