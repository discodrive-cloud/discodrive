package photos

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// patternImage draws a smooth non-flat pattern in normalized coordinates, so
// the same picture can be rendered at any size. seed varies the pattern.
func patternImage(w, h int, seed float64, gain float64) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			u := (float64(x) + 0.5) / float64(w)
			v := (float64(y) + 0.5) / float64(h)
			r := 128 + 100*math.Sin(2*math.Pi*(1.3+seed)*u+seed*v*5)
			g := 128 + 100*math.Cos(2*math.Pi*(0.9+seed*0.7)*u*v+seed)
			b := 128 + 100*math.Sin(2*math.Pi*(2.1+seed*0.3)*v+u*seed*7)
			img.SetRGBA(x, y, color.RGBA{clamp8(r * gain), clamp8(g * gain), clamp8(b * gain), 255})
		}
	}
	return img
}

func clamp8(f float64) uint8 {
	if f < 0 {
		return 0
	}
	if f > 255 {
		return 255
	}
	return uint8(f)
}

func TestDHash_SameImage(t *testing.T) {
	img := patternImage(400, 300, 1, 1)
	if d := Hamming(DHash(img), DHash(img)); d != 0 {
		t.Fatalf("Hamming = %d, want 0", d)
	}
	if DHash(img) == 0 {
		t.Fatal("hash of a patterned image must not be 0")
	}
}

func TestDHash_Reencoded(t *testing.T) {
	img := patternImage(400, 300, 1, 1)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 55}); err != nil {
		t.Fatal(err)
	}
	dec, err := jpeg.Decode(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if d := Hamming(DHash(img), DHash(dec)); d > 4 {
		t.Fatalf("Hamming = %d, want <= 4", d)
	}
}

func TestDHash_Resized(t *testing.T) {
	a := patternImage(800, 600, 1, 1)
	b := patternImage(400, 300, 1, 1)
	if d := Hamming(DHash(a), DHash(b)); d > 4 {
		t.Fatalf("Hamming = %d, want <= 4", d)
	}
}

func TestDHash_Different(t *testing.T) {
	a := patternImage(400, 300, 1, 1)
	b := patternImage(400, 300, 3.7, 1)
	if d := Hamming(DHash(a), DHash(b)); d < 20 {
		t.Fatalf("patterns: Hamming = %d, want >= 20", d)
	}
	// Opposite horizontal gradients flip every bit.
	l := image.NewRGBA(image.Rect(0, 0, 90, 80))
	r := image.NewRGBA(image.Rect(0, 0, 90, 80))
	for y := 0; y < 80; y++ {
		for x := 0; x < 90; x++ {
			l.Set(x, y, color.Gray{uint8(x * 2)})
			r.Set(x, y, color.Gray{uint8(255 - x*2)})
		}
	}
	if d := Hamming(DHash(l), DHash(r)); d < 20 {
		t.Fatalf("gradients: Hamming = %d, want >= 20", d)
	}
}

func TestDHash_Brightness(t *testing.T) {
	a := patternImage(400, 300, 1, 1)
	b := patternImage(400, 300, 1, 1.1)
	if d := Hamming(DHash(a), DHash(b)); d > 6 {
		t.Fatalf("Hamming = %d, want <= 6", d)
	}
}

func TestDHashFile(t *testing.T) {
	img := patternImage(400, 400, 2, 1)
	path := filepath.Join(t.TempDir(), "t.jpg")
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	h, flat, err := DHashFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if d := Hamming(h, DHash(img)); d > 4 {
		t.Fatalf("Hamming = %d, want <= 4", d)
	}
	if flat {
		t.Fatal("a patterned thumbnail reported flat")
	}
	if _, _, err := DHashFile(filepath.Join(t.TempDir(), "missing.jpg")); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func fillImage(w, h int, f func(x, y int) color.Color) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, f(x, y))
		}
	}
	return img
}

func TestDHashFlat(t *testing.T) {
	flat := map[string]image.Image{
		"black frame":  fillImage(320, 240, func(int, int) color.Color { return color.Black }),
		"solid colour": fillImage(320, 240, func(int, int) color.Color { return color.RGBA{200, 40, 90, 255} }),
		// Luma 0..12 across the width: neighbouring cells differ by ~1.4 levels.
		"dark gradient": fillImage(320, 240, func(x, _ int) color.Color { return color.Gray{uint8(x * 13 / 320)} }),
	}
	for name, img := range flat {
		if _, f := DHashFlat(img); !f {
			t.Errorf("%s: not flat", name)
		}
		// Re-encoded as the thumbnail it would be: JPEG noise must not make it look busy.
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 75}); err != nil {
			t.Fatal(err)
		}
		dec, err := jpeg.Decode(&buf)
		if err != nil {
			t.Fatal(err)
		}
		if _, f := DHashFlat(dec); !f {
			t.Errorf("%s as JPEG: not flat", name)
		}
	}
	for _, seed := range []float64{1, 2, 3.7} {
		if _, f := DHashFlat(patternImage(400, 300, seed, 1)); f {
			t.Errorf("pattern %v: flat", seed)
		}
	}
	// A dark but real picture (a night shot: dim scene, contrast kept) is not flat.
	if _, f := DHashFlat(patternImage(400, 300, 2, 0.15)); f {
		t.Error("dim pattern: flat")
	}
}
