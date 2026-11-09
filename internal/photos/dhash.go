package photos

import (
	"image"
	_ "image/gif" // register decoders for DHashFile
	_ "image/jpeg"
	_ "image/png"
	"math/bits"
	"os"
)

const (
	dhashW = 9 // columns of the reduced image; 8 comparisons per row
	dhashH = 8
)

// DHash computes a 64-bit difference hash of img: the picture is converted to
// grayscale, reduced to 9x8 by area averaging, and each bit says whether a
// pixel is brighter than its right neighbour. Visually similar images (resized,
// re-encoded, slightly brightened) get hashes with a small Hamming distance.
// A flat image hashes to (nearly) 0; DHashFlat tells such pictures apart.
func DHash(img image.Image) uint64 {
	h, _ := DHashFlat(img)
	return h
}

const (
	// flatStep is the brightness difference (on the 0..255 luma scale) between two
	// neighbouring cells below which their bit is decided by noise, not by the picture.
	flatStep = 2.0
	// flatMinBits: a picture with fewer neighbouring cells that differ by flatStep or more
	// than this is flat. Its hash is mostly noise (a black frame, a solid colour, a dark
	// smooth gradient all hash near 0), and two different such pictures would look like
	// duplicates, so flat cards are left out of the search.
	flatMinBits = 16
)

// DHashFlat is DHash plus whether the picture is flat: fewer than flatMinBits of its 64
// comparisons differ by at least flatStep luma levels.
func DHashFlat(img image.Image) (uint64, bool) {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return 0, true
	}
	var cells [dhashH][dhashW]float64
	for cy := 0; cy < dhashH; cy++ {
		y0, y1 := cellRange(cy, dhashH, h)
		for cx := 0; cx < dhashW; cx++ {
			x0, x1 := cellRange(cx, dhashW, w)
			var sum float64
			for y := y0; y < y1; y++ {
				for x := x0; x < x1; x++ {
					r, g, bl, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
					sum += 0.299*float64(r) + 0.587*float64(g) + 0.114*float64(bl)
				}
			}
			cells[cy][cx] = sum / float64((y1-y0)*(x1-x0)) / 257 // 0..255
		}
	}
	var hash uint64
	decisive := 0
	for y := 0; y < dhashH; y++ {
		for x := 0; x < dhashW-1; x++ {
			hash <<= 1
			d := cells[y][x] - cells[y][x+1]
			if d > 0 {
				hash |= 1
			}
			if d >= flatStep || d <= -flatStep {
				decisive++
			}
		}
	}
	return hash, decisive < flatMinBits
}

// cellRange returns the half-open source range [lo, hi) of cell i out of n
// cells covering size pixels; it is never empty.
func cellRange(i, n, size int) (int, int) {
	lo := i * size / n
	hi := (i + 1) * size / n
	if hi <= lo {
		hi = lo + 1
	}
	return lo, hi
}

// Hamming returns the number of differing bits between two hashes.
func Hamming(a, b uint64) int {
	return bits.OnesCount64(a ^ b)
}

// DHashFile decodes the image at path (a grid thumbnail JPEG) and returns its
// DHash and whether the picture is flat (see DHashFlat).
func DHashFile(path string) (uint64, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, false, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return 0, false, err
	}
	h, flat := DHashFlat(img)
	return h, flat, nil
}
