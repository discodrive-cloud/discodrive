package ebook

import "testing"

// pdfcpu loads every stream of a PDF to read its Info dictionary: a 100 MB scan cost
// ~100 MB of heap, and parallel scans OOM-killed a 256 MB box. Above the cap the book
// keeps its filename as title instead.
func TestParsePDFSkipsFilesAboveCap(t *testing.T) {
	path := writeTempPDF(t, minimalPDFWithInfo, ".pdf")
	old := maxPDFMetaBytes
	maxPDFMetaBytes = int64(len(minimalPDFWithInfo)) - 1
	t.Cleanup(func() { maxPDFMetaBytes = old })

	m, err := parsePDF(path)
	if err != nil {
		t.Fatalf("parsePDF: %v", err)
	}
	if m.Title != "" || len(m.Authors) != 0 {
		t.Fatalf("a PDF above the cap was parsed: %+v", m)
	}
}
