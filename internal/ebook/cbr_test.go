package ebook

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"os"
	"path/filepath"
	"testing"

	"github.com/nwaples/rardecode/v2"
)

// There is no rar tool to produce fixtures, so the tests write minimal RAR5 archives
// themselves: stored (uncompressed) entries, plus a header that declares compression
// with a chosen dictionary size, which is all the dictionary check looks at.

func rarVint(v uint64) []byte {
	var b []byte
	for {
		c := byte(v & 0x7f)
		v >>= 7
		if v != 0 {
			b = append(b, c|0x80)
			continue
		}
		return append(b, c)
	}
}

// rarBlock frames one RAR5 header: CRC32, size, then type, flags and body; data
// (if any) follows the header.
func rarBlock(typ, flags uint64, body, data []byte) []byte {
	var h []byte
	h = append(h, rarVint(typ)...)
	if data != nil {
		flags |= 0x0002 // data area present
	}
	h = append(h, rarVint(flags)...)
	if data != nil {
		h = append(h, rarVint(uint64(len(data)))...)
	}
	h = append(h, body...)
	sized := append(rarVint(uint64(len(h))), h...)
	var out []byte
	out = binary.LittleEndian.AppendUint32(out, crc32.ChecksumIEEE(sized))
	out = append(out, sized...)
	return append(out, data...)
}

type rarEntry struct {
	name string
	data []byte
	// compInfo overrides the compression-info field; 0 means stored.
	compInfo uint64
	// unpacked overrides the declared unpacked size; 0 means len(data).
	unpacked uint64
}

func writeRAR5(t *testing.T, entries []rarEntry) string {
	t.Helper()
	var buf bytes.Buffer
	buf.Write([]byte{0x52, 0x61, 0x72, 0x21, 0x1A, 0x07, 0x01, 0x00})
	buf.Write(rarBlock(1, 0, rarVint(0), nil)) // main archive header
	for _, e := range entries {
		size := e.unpacked
		if size == 0 {
			size = uint64(len(e.data))
		}
		var body []byte
		body = append(body, rarVint(0x0004)...) // file flags: CRC32 present
		body = append(body, rarVint(size)...)
		body = append(body, rarVint(0x20)...) // attributes
		body = binary.LittleEndian.AppendUint32(body, crc32.ChecksumIEEE(e.data))
		body = append(body, rarVint(e.compInfo)...)
		body = append(body, rarVint(1)...) // host OS: unix
		body = append(body, rarVint(uint64(len(e.name)))...)
		body = append(body, e.name...)
		buf.Write(rarBlock(2, 0, body, e.data))
	}
	buf.Write(rarBlock(5, 0, rarVint(0), nil)) // end of archive
	p := filepath.Join(t.TempDir(), "comic.cbr")
	if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// A stored CBR yields its ComicInfo metadata and the lexicographically first image.
func TestParseCBRPicksFirstImage(t *testing.T) {
	p := writeRAR5(t, []rarEntry{
		{name: "page-003.png", data: []byte("\x89PNG three")},
		{name: "ComicInfo.xml", data: []byte(comicInfoXML)},
		{name: "page-001.jpg", data: []byte("\xff\xd8\xff one")},
		{name: "page-002.jpg", data: []byte("\xff\xd8\xff two")},
	})
	m, err := parseComic(p)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if string(m.CoverData) != "\xff\xd8\xff one" || m.CoverType != "image/jpeg" {
		t.Fatalf("cover = %q (%s), want page-001.jpg", m.CoverData, m.CoverType)
	}
	if m.Title == "" {
		t.Fatalf("ComicInfo metadata not read: %+v", m)
	}
}

// GO-2025-4020: the decode window is sized from the archive header before any data is
// read. A header asking for a 4 GB dictionary must be refused, not allocated.
func TestParseCBRRefusesHugeDictionary(t *testing.T) {
	const (
		methodNormal = 3 << 7   // compressed, so a window is needed
		dict4GB      = 15 << 10 // 128 KiB << 15
		dict128MB    = 10 << 10 // over our 64 MiB cap, under the library's 4 GB default
	)
	for _, dict := range []uint64{dict4GB, dict128MB} {
		p := writeRAR5(t, []rarEntry{{
			name: "page-001.jpg", data: []byte("x"),
			compInfo: methodNormal | dict, unpacked: 1 << 40,
		}})
		_, err := parseComic(p)
		if !errors.Is(err, rardecode.ErrDictionaryTooLarge) {
			t.Fatalf("dictionary %d KiB: err = %v, want ErrDictionaryTooLarge", 128<<(dict>>10), err)
		}
	}
}

// A dictionary within the usual RAR5 default (32 MiB) is still accepted.
func TestCBRDictionaryCapAllowsDefaults(t *testing.T) {
	if maxRARDictBytes < 32<<20 {
		t.Fatalf("maxRARDictBytes = %d, below the RAR5 default dictionary", maxRARDictBytes)
	}
}
