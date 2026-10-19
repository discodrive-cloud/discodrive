package tagwrite

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func box(name string, body ...[]byte) []byte {
	b := bytes.Join(body, nil)
	out := binary.BigEndian.AppendUint32(nil, uint32(8+len(b)))
	return append(append(out, name...), b...)
}

var ftyp = box("ftyp", []byte("M4A \x00\x00\x00\x00"))

func writeTemp(t *testing.T, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "x.m4a")
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// A 24-byte file with a zero-size box sent go-mp4tag into endless recursion, and a stack
// overflow ends the whole server. The parsers must never see it.
func TestCheckMP4_RefusesTreesTheParsersCannotWalk(t *testing.T) {
	deep := box("free")
	for i := 0; i < 40; i++ {
		deep = box("moov", deep)
	}
	for name, data := range map[string][]byte{
		"zero size":      append(append([]byte{}, ftyp...), 0, 0, 0, 0, 'f', 'r', 'e', 'e'),
		"64-bit size":    append(append([]byte{}, ftyp...), 0, 0, 0, 1, 'm', 'd', 'a', 't', 0, 0, 0, 0, 0, 0, 0, 16),
		"past parent":    append(append([]byte{}, ftyp...), box("moov", []byte{0, 0, 0, 64, 'u', 'd', 't', 'a'})...),
		"past file end":  append(append([]byte{}, ftyp...), 0, 0, 1, 0, 'm', 'd', 'a', 't'),
		"trailing bytes": append(append([]byte{}, ftyp...), box("moov", box("udta"), []byte{0, 0, 0, 0})...),
		"too deep":       append(append([]byte{}, ftyp...), deep...),
	} {
		p := writeTemp(t, data)
		if err := CheckMP4File(p); !errors.Is(err, ErrBadMP4) {
			t.Errorf("%s: CheckMP4 = %v, want ErrBadMP4", name, err)
		}
		if _, _, err := (mp4Writer{}).Read(p); !errors.Is(err, ErrBadMP4) {
			t.Errorf("%s: Read = %v, want ErrBadMP4", name, err)
		}
		if _, _, ok := (mp4Writer{}).Cover(p); ok {
			t.Errorf("%s: Cover reported a picture", name)
		}
		if err := (mp4Writer{}).Apply(p, Tags{}, CoverKeep, nil); !errors.Is(err, ErrBadMP4) {
			t.Errorf("%s: Apply = %v, want ErrBadMP4", name, err)
		}
		f, _ := os.Open(p)
		if _, err := ReadFrom(f); !errors.Is(err, ErrBadMP4) {
			t.Errorf("%s: ReadFrom = %v, want ErrBadMP4", name, err)
		}
		f.Close()
	}
}

// dhowden/tag picks its MP4 parser by content, so a bad tree under another extension is
// refused too; anything that is not MP4 is left to the parsers.
func TestCheckMP4_OnlyJudgesMP4(t *testing.T) {
	for _, name := range []string{"sample.mp3", "sample.flac", "sample.ogg", "sample.m4a"} {
		if err := CheckMP4File(filepath.Join("testdata", name)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := CheckMP4(bytes.NewReader([]byte("ID3"))); err != nil {
		t.Errorf("short non-MP4 input: %v", err)
	}
	ilst := box("ilst", box("\xa9nam", box("data", []byte("x"))))
	meta := box("meta", []byte{0, 0, 0, 0}, ilst)
	good := append(append([]byte{}, ftyp...), box("moov", box("udta", meta))...)
	if err := CheckMP4(bytes.NewReader(good)); err != nil {
		t.Errorf("well-formed tree: %v", err)
	}
}
