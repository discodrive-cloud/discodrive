package ebook

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// allocDuring reports the bytes allocated while fn runs.
func allocDuring(fn func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	fn()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// writeZeros streams n zero bytes into w without holding them in memory.
func writeZeros(t *testing.T, w io.Writer, n int64) {
	t.Helper()
	if _, err := io.CopyN(w, zeroReader{}, n); err != nil {
		t.Fatal(err)
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

// A cover entry that inflates to 256 MiB (a zip bomb) must neither be buffered
// nor stop the book from being indexed.
func TestParseEPUB_CoverBombIsNotBuffered(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bomb.epub")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := zip.NewWriter(f)
	cw, _ := w.Create("META-INF/container.xml")
	cw.Write([]byte(`<container><rootfiles><rootfile full-path="content.opf"/></rootfiles></container>`))
	ow, _ := w.Create("content.opf")
	ow.Write([]byte(`<package xmlns="http://www.idpf.org/2007/opf" xmlns:dc="http://purl.org/dc/elements/1.1/">
<metadata><dc:title>Bomb</dc:title></metadata>
<manifest><item id="c" href="cover.jpg" media-type="image/jpeg" properties="cover-image"/></manifest>
</package>`))
	bw, _ := w.Create("cover.jpg")
	writeZeros(t, bw, 256<<20)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()

	var m Meta
	alloc := allocDuring(func() {
		m, err = parseEPUB(path)
	})
	if err != nil {
		t.Fatalf("parseEPUB: %v", err)
	}
	if m.Title != "Bomb" {
		t.Errorf("title = %q, want Bomb", m.Title)
	}
	if m.CoverData != nil {
		t.Errorf("cover of %d bytes was buffered, want it dropped", len(m.CoverData))
	}
	if alloc > 16<<20 {
		t.Errorf("parse allocated %d MiB, want a bounded amount", alloc>>20)
	}
}

// An oversized OPF is refused instead of being read whole.
func TestParseEPUB_OversizedOPFRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "opf.epub")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := zip.NewWriter(f)
	cw, _ := w.Create("META-INF/container.xml")
	cw.Write([]byte(`<container><rootfiles><rootfile full-path="content.opf"/></rootfiles></container>`))
	ow, _ := w.Create("content.opf")
	ow.Write([]byte(`<package><metadata><title>x</title></metadata><!--`))
	writeZeros(t, ow, 64<<20)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()

	alloc := allocDuring(func() { _, err = parseEPUB(path) })
	if err == nil {
		t.Fatal("parseEPUB accepted a 64 MiB OPF")
	}
	if alloc > 16<<20 {
		t.Errorf("parse allocated %d MiB, want a bounded amount", alloc>>20)
	}
}

// fb2WithBody builds an FB2 whose body holds bodyBytes of text before the cover
// binary, the usual layout (description, body, binaries).
func fb2WithBody(w io.Writer, t *testing.T, bodyBytes int64) {
	t.Helper()
	io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?>
<FictionBook xmlns="http://www.gribuser.ru/xml/fictionbook/2.0" xmlns:l="http://www.w3.org/1999/xlink">
<description><title-info><book-title>Big Book</book-title><lang>ru</lang>
<coverpage><image l:href="#c.jpg"/></coverpage></title-info></description>
<body>`)
	line := "<p>" + strings.Repeat("слово ", 150) + "</p>\n"
	for n := int64(0); n < bodyBytes; n += int64(len(line)) {
		io.WriteString(w, line)
	}
	io.WriteString(w, `</body><binary id="c.jpg" content-type="image/jpeg">/9j/4AAQ</binary></FictionBook>`)
}

// A 200 MiB fb2.zip is streamed: memory stays bounded and the description,
// which comes first, is still indexed.
func TestParseFB2Zip_HugeEntryIsStreamed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.fb2.zip")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := zip.NewWriter(f)
	ew, _ := w.Create("big.fb2")
	fb2WithBody(ew, t, 200<<20)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()

	var m Meta
	alloc := allocDuring(func() { m, err = parseFB2Zip(path) })
	if err != nil {
		t.Fatalf("parseFB2Zip: %v", err)
	}
	if m.Title != "Big Book" || m.Language != "ru" {
		t.Errorf("meta = %q/%q, want Big Book/ru", m.Title, m.Language)
	}
	if alloc > 64<<20 {
		t.Errorf("parse allocated %d MiB, want a bounded amount", alloc>>20)
	}
}

// Within the cap the cover after the body is still found.
func TestParseFB2_CoverAfterBody(t *testing.T) {
	var buf bytes.Buffer
	fb2WithBody(&buf, t, 1<<20)
	m, err := parseFB2Bytes(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if m.Title != "Big Book" || m.CoverType != "image/jpeg" || len(m.CoverData) == 0 {
		t.Errorf("meta = %q cover %q/%d", m.Title, m.CoverType, len(m.CoverData))
	}
}

// A cover binary that decodes past maxEbookCoverBytes is dropped, the rest kept.
func TestParseFB2_OversizedCoverDropped(t *testing.T) {
	var buf bytes.Buffer
	buf.WriteString(`<FictionBook xmlns:l="http://www.w3.org/1999/xlink"><description><title-info>
<book-title>T</book-title><coverpage><image l:href="#c"/></coverpage></title-info></description>
<binary id="c" content-type="image/png">`)
	buf.WriteString(strings.Repeat("AAAA", int(maxEbookCoverBytes/3)+16))
	buf.WriteString(`</binary></FictionBook>`)
	m, err := parseFB2Bytes(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if m.Title != "T" || m.CoverData != nil {
		t.Errorf("title %q, cover %d bytes; want T and no cover", m.Title, len(m.CoverData))
	}
}
