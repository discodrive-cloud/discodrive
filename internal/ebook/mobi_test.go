package ebook

import (
	"encoding/binary"
	"os"
	"runtime"
	"testing"
)

// buildMOBI constructs a minimal but structurally valid MOBI byte buffer
// suitable for testing parseMOBIBytes. Layout:
//
//   - PalmDB header (78 bytes): name[32] + misc fields + numRecords=1
//   - Record-info list (1 entry × 8 bytes): offset pointing to record0
//   - record0:
//   - PalmDOC header (16 bytes)
//   - MOBI header (148 bytes, length=148): magic + length + ... + fullNameOffset + fullNameLength + firstImageIndex + EXTH flags
//   - EXTH block: magic + headerLen + recordCount + records (author, publisher, subject)
//   - Full Name bytes ("Test Book Title")
//
// All integers are big-endian (PalmDB / MOBI convention).
func buildMOBI(t *testing.T) []byte {
	t.Helper()

	// ---- constants ----
	const (
		palmDBHeaderSize = 78
		recInfoEntrySize = 8
		numRecords       = 1

		palmDOCSize = 16
		mobiMagic   = "MOBI"
		mobiHdrLen  = 148 // covers all fields up through EXTH flags (0x80+4=132) + extra
	)

	title := "Test Book Title"
	authorName := "Jane Doe"
	publisher := "Test Publisher"
	subject := "Fiction"

	// ---- Build EXTH block ----
	// EXTH records: each = type(4) + len(4) + data.
	buildEXTH := func(typ uint32, payload []byte) []byte {
		recLen := uint32(8 + len(payload))
		b := make([]byte, recLen)
		binary.BigEndian.PutUint32(b[0:], typ)
		binary.BigEndian.PutUint32(b[4:], recLen)
		copy(b[8:], payload)
		return b
	}

	exthRec100 := buildEXTH(100, []byte(authorName)) // author
	exthRec101 := buildEXTH(101, []byte(publisher))  // publisher
	exthRec105 := buildEXTH(105, []byte(subject))    // subject

	exthRecords := make([]byte, 0)
	exthRecords = append(exthRecords, exthRec100...)
	exthRecords = append(exthRecords, exthRec101...)
	exthRecords = append(exthRecords, exthRec105...)

	// EXTH header: "EXTH" + totalLen(4) + numRec(4) + records + padding to 4-byte boundary.
	exthHeaderFixed := 12 // "EXTH" + len + count
	exthDataLen := exthHeaderFixed + len(exthRecords)
	// Pad to 4-byte boundary.
	if exthDataLen%4 != 0 {
		exthDataLen += 4 - exthDataLen%4
	}
	exthBlock := make([]byte, exthDataLen)
	copy(exthBlock[0:], "EXTH")
	binary.BigEndian.PutUint32(exthBlock[4:], uint32(exthDataLen))
	binary.BigEndian.PutUint32(exthBlock[8:], 3) // 3 records
	copy(exthBlock[12:], exthRecords)

	// ---- Compute record0 layout ----
	// record0 = PalmDOC(16) + MOBI header(mobiHdrLen) + EXTH block + title bytes
	titleBytes := []byte(title)

	// fullNameOffset is relative to the start of record0.
	fullNameOffset := uint32(palmDOCSize + mobiHdrLen + len(exthBlock))
	fullNameLength := uint32(len(titleBytes))

	// ---- Build MOBI header (mobiHdrLen bytes) ----
	mobiHdr := make([]byte, mobiHdrLen)
	copy(mobiHdr[0:], mobiMagic)                                // "MOBI" magic
	binary.BigEndian.PutUint32(mobiHdr[4:], uint32(mobiHdrLen)) // header length
	// mobiHdr[8:12] = mobiType (uint32) — 0x002 = Mobipocket Book
	binary.BigEndian.PutUint32(mobiHdr[8:], 2)
	// mobiHdr[12:16] = textEncoding — 65001 = UTF-8
	binary.BigEndian.PutUint32(mobiHdr[12:], 65001)
	// Offsets 16..0x53: various fields, leave as zero.
	// fullNameOffset at mobiHdr[0x54] = mobiHdr[84]
	binary.BigEndian.PutUint32(mobiHdr[0x54:], fullNameOffset)
	// fullNameLength at mobiHdr[0x58] = mobiHdr[88]
	binary.BigEndian.PutUint32(mobiHdr[0x58:], fullNameLength)
	// firstImageIndex at mobiHdr[0x6C] = mobiHdr[108]: set to 0xFFFFFFFF (no images)
	binary.BigEndian.PutUint32(mobiHdr[0x6C:], 0xFFFFFFFF)
	// EXTH flags at mobiHdr[0x80] = mobiHdr[128]: bit 0x40 = EXTH present
	binary.BigEndian.PutUint32(mobiHdr[0x80:], 0x40)

	// ---- Build PalmDOC header (16 bytes) ----
	palmDOC := make([]byte, palmDOCSize)
	binary.BigEndian.PutUint16(palmDOC[0:], 1) // compression: PalmDOC
	// unused: palmDOC[2:4] = 0
	binary.BigEndian.PutUint32(palmDOC[4:], 0)     // text length
	binary.BigEndian.PutUint16(palmDOC[8:], 1)     // record count
	binary.BigEndian.PutUint16(palmDOC[10:], 4096) // record size
	binary.BigEndian.PutUint16(palmDOC[12:], 0)    // encryption type: none

	// ---- Assemble record0 ----
	record0 := make([]byte, 0, palmDOCSize+mobiHdrLen+len(exthBlock)+len(titleBytes))
	record0 = append(record0, palmDOC...)
	record0 = append(record0, mobiHdr...)
	record0 = append(record0, exthBlock...)
	record0 = append(record0, titleBytes...)

	// ---- Assemble full file ----
	// record0 starts immediately after PalmDB header + record-info list.
	rec0StartOffset := uint32(palmDBHeaderSize + numRecords*recInfoEntrySize)

	// Build PalmDB header (78 bytes).
	palmDBHdr := make([]byte, palmDBHeaderSize)
	// name[0:32]: database name (null-padded)
	copy(palmDBHdr[0:], "TestBook")
	// attributes[32:34], version[34:36], creation/modification/backup/modificationNumber dates: leave zero
	// appInfoID[40:44], sortInfoID[44:48]: leave zero
	// type[48:52] = "BOOK", creator[52:56] = "MOBI"
	copy(palmDBHdr[48:], "BOOK")
	copy(palmDBHdr[52:], "MOBI")
	// uniqueIDSeed[56:60], nextRecordListID[60:64]: leave zero
	// numRecords at [76:78]
	binary.BigEndian.PutUint16(palmDBHdr[76:], uint16(numRecords))

	// Record-info list: 1 entry.
	recInfo := make([]byte, numRecords*recInfoEntrySize)
	binary.BigEndian.PutUint32(recInfo[0:], rec0StartOffset)
	// attribs + uniqueID: leave zero

	file := make([]byte, 0, int(rec0StartOffset)+len(record0))
	file = append(file, palmDBHdr...)
	file = append(file, recInfo...)
	file = append(file, record0...)

	return file
}

func TestParseMOBI_Metadata(t *testing.T) {
	data := buildMOBI(t)

	m, err := parseMOBIBytes(data)
	if err != nil {
		t.Fatalf("parseMOBIBytes returned error: %v", err)
	}

	if m.Title != "Test Book Title" {
		t.Errorf("Title = %q, want %q", m.Title, "Test Book Title")
	}

	if len(m.Authors) == 0 {
		t.Fatalf("Authors is empty, want at least 1")
	}
	if m.Authors[0].Name != "Jane Doe" {
		t.Errorf("Authors[0].Name = %q, want %q", m.Authors[0].Name, "Jane Doe")
	}
	if m.Authors[0].SortName != "jane doe" {
		t.Errorf("Authors[0].SortName = %q, want %q", m.Authors[0].SortName, "jane doe")
	}

	if m.Publisher != "Test Publisher" {
		t.Errorf("Publisher = %q, want %q", m.Publisher, "Test Publisher")
	}

	if len(m.Tags) == 0 || m.Tags[0] != "Fiction" {
		t.Errorf("Tags = %v, want [Fiction]", m.Tags)
	}

	// Cover should be absent (firstImageIndex = 0xFFFFFFFF, no image records).
	if m.CoverData != nil {
		t.Errorf("expected no CoverData, got %d bytes", len(m.CoverData))
	}
}

func TestParseMOBI_Malformed_NoPath(t *testing.T) {
	// Various truncated / garbage inputs must return an error and never panic.
	cases := []struct {
		name string
		data []byte
	}{
		{"empty", []byte{}},
		{"too short", []byte{0x00, 0x01, 0x02}},
		{"random garbage", []byte("this is not a mobi file at all, just random ASCII bytes padded")},
		{"all zeros 100 bytes", make([]byte, 100)},
		{"truncated mid-header", make([]byte, 80)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("parseMOBIBytes panicked: %v", r)
				}
			}()
			_, err := parseMOBIBytes(tc.data)
			if err == nil {
				t.Errorf("expected error for malformed input %q, got nil", tc.name)
			}
		})
	}
}

func TestParseMOBI_ViaReadMeta(t *testing.T) {
	// Write the fixture to a temp file and verify ReadMeta dispatches correctly.
	data := buildMOBI(t)
	tmp, err := os.CreateTemp(t.TempDir(), "test-*.mobi")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	if _, err := tmp.Write(data); err != nil {
		t.Fatalf("Write: %v", err)
	}
	tmp.Close()
	f := tmp.Name()

	m, err := ReadMeta(f)
	if err != nil {
		t.Fatalf("ReadMeta(%q) error: %v", f, err)
	}
	if m.Title != "Test Book Title" {
		t.Errorf("ReadMeta Title = %q, want %q", m.Title, "Test Book Title")
	}
	if m.Format != "mobi" {
		t.Errorf("ReadMeta Format = %q, want mobi", m.Format)
	}
	if m.ContentType != "application/x-mobipocket-ebook" {
		t.Errorf("ReadMeta ContentType = %q", m.ContentType)
	}
}

// A large MOBI must not be read into memory: only the record table, record 0 and the
// cover record are, each bounded. The file here is 256 MiB (sparse) with its single
// record spanning all of it; parsing may refuse it, but must not allocate it.
func TestParseMOBIDoesNotReadWholeFile(t *testing.T) {
	p := t.TempDir() + "/big.mobi"
	if err := os.WriteFile(p, buildMOBI(t), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(p, 256<<20); err != nil {
		t.Fatal(err)
	}
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	_, _ = parseMOBI(p)
	runtime.ReadMemStats(&after)
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 16<<20 {
		t.Fatalf("parsing a 256 MiB MOBI allocated %d MiB", alloc>>20)
	}
}
