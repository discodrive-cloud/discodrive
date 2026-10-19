package tagwrite

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/dhowden/tag"
)

// ErrBadMP4 is returned for an MP4 file whose box tree the tag parsers cannot be trusted
// with.
var ErrBadMP4 = errors.New("tagwrite: malformed mp4 box tree")

// Both MP4 tag parsers walk the box tree recursively on the sizes found in the file:
// go-mp4tag recurses once per box, sibling or child, and loops forever on a box that
// does not advance (size 0); dhowden/tag recurses once per container header. A recursion
// that deep overflows the goroutine stack, which is fatal to the whole process, not a
// panic recover can catch. So the tree is walked here first, iteratively, and the file is
// handed to a parser only if every box is well formed.
const (
	maxMP4Boxes = 100000
	maxMP4Depth = 32
)

// mp4Containers are the boxes go-mp4tag descends into (its own list, names as its
// readBoxName spells them); dhowden/tag descends into a subset of them.
var mp4Containers = map[string]bool{
	"moov": true, "udta": true, "meta": true, "ilst": true, "----": true, "(c)alb": true,
	"aART": true, "(c)art": true, "(c)nam": true, "(c)cmt": true, "(c)gen": true, "gnre": true,
	"(c)wrt": true, "(c)con": true, "cprt": true, "desc": true, "(c)lyr": true, "(c)nrt": true,
	"(c)pub": true, "trkn": true, "covr": true, "(c)day": true, "disk": true, "(c)too": true,
	"trak": true, "mdia": true, "minf": true, "stbl": true, "rtng": true, "plID": true,
	"atID": true, "tmpo": true, "sonm": true, "soal": true, "soar": true, "soco": true,
	"soaa": true,
}

// CheckMP4 validates the box tree of r when it holds an MP4 file (an "ftyp" box at the
// start, which is what both parsers key on) and returns nil for anything else. Every box
// must be at least 8 bytes and lie inside its parent, and the children of a container
// must fill it exactly. The 64-bit and to-end-of-file size forms (1 and 0) are refused:
// go-mp4tag reads them as literal sizes. r is left at offset 0.
func CheckMP4(r io.ReadSeeker) error {
	size, err := r.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}
	defer r.Seek(0, io.SeekStart) //nolint:errcheck
	hdr := make([]byte, 8)
	if size < 8 {
		return nil
	}
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if _, err := io.ReadFull(r, hdr); err != nil {
		return err
	}
	if string(hdr[4:8]) != "ftyp" {
		return nil
	}

	type span struct {
		start, end int64
		depth      int
	}
	stack := []span{{0, size, 0}}
	boxes := 0
	for len(stack) > 0 {
		s := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if s.depth > maxMP4Depth {
			return ErrBadMP4
		}
		for pos := s.start; pos < s.end; {
			if boxes++; boxes > maxMP4Boxes || s.end-pos < 8 {
				return ErrBadMP4
			}
			if _, err := r.Seek(pos, io.SeekStart); err != nil {
				return err
			}
			if _, err := io.ReadFull(r, hdr); err != nil {
				return ErrBadMP4
			}
			n := int64(binary.BigEndian.Uint32(hdr[:4]))
			if n < 8 || n > s.end-pos {
				return ErrBadMP4
			}
			name := string(hdr[4:8])
			if hdr[4] == 0xA9 {
				name = "(c)" + strings.ToLower(name[1:])
			}
			if mp4Containers[name] {
				off := int64(8)
				if name == "meta" {
					off = 12 // version and flags precede the children
				}
				if n < off {
					return ErrBadMP4
				}
				stack = append(stack, span{pos + off, pos + n, s.depth + 1})
			}
			pos += n
		}
	}
	return nil
}

// CheckMP4File is CheckMP4 for a file on disk.
func CheckMP4File(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return CheckMP4(f)
}

// ReadFrom is tag.ReadFrom behind CheckMP4. dhowden/tag picks the parser from the
// content, not the extension, so every caller goes through here.
func ReadFrom(r io.ReadSeeker) (tag.Metadata, error) {
	if err := CheckMP4(r); err != nil {
		return nil, err
	}
	return tag.ReadFrom(r)
}
