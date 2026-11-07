package links

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"
)

func pngOf(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		img.Set(x, 0, color.RGBA{255, 0, 0, 255})
	}
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return b.Bytes()
}

func TestMakeThumbResizes(t *testing.T) {
	out, err := MakeThumb(bytes.NewReader(pngOf(1000, 500)))
	if err != nil {
		t.Fatal(err)
	}
	img, err := jpeg.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("thumb is not a JPEG: %v", err)
	}
	if b := img.Bounds(); b.Dx() != 160 || b.Dy() != 80 {
		t.Fatalf("thumb is %dx%d, want 160x80", b.Dx(), b.Dy())
	}
}

func TestMakeThumbSmallStaysSmall(t *testing.T) {
	out, err := MakeThumb(bytes.NewReader(pngOf(40, 20)))
	if err != nil {
		t.Fatal(err)
	}
	img, _ := jpeg.Decode(bytes.NewReader(out))
	if b := img.Bounds(); b.Dx() != 40 || b.Dy() != 20 {
		t.Fatalf("small image must not be upscaled, got %dx%d", b.Dx(), b.Dy())
	}
}

func TestMakeThumbRejectsNonRaster(t *testing.T) {
	for name, body := range map[string]string{
		"svg":  `<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"></svg>`,
		"html": `<!doctype html><html><script>alert(1)</script>`,
		"text": "hello",
	} {
		if _, err := MakeThumb(strings.NewReader(body)); !errors.Is(err, ErrNotImage) {
			t.Errorf("%s: err = %v, want ErrNotImage", name, err)
		}
	}
}

// pngHeaderClaiming returns a PNG signature and IHDR declaring w×h, with no pixel data:
// a decoder that trusted it would try to allocate w*h*4 bytes.
func pngHeaderClaiming(w, h uint32) []byte {
	var b bytes.Buffer
	b.WriteString("\x89PNG\r\n\x1a\n")
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], w)
	binary.BigEndian.PutUint32(ihdr[4:], h)
	ihdr[8], ihdr[9] = 8, 6 // 8-bit RGBA
	chunk := append([]byte("IHDR"), ihdr...)
	_ = binary.Write(&b, binary.BigEndian, uint32(13))
	b.Write(chunk)
	_ = binary.Write(&b, binary.BigEndian, crc32.ChecksumIEEE(chunk))
	return b.Bytes()
}

func TestMakeThumbRejectsBombs(t *testing.T) {
	if _, err := MakeThumb(bytes.NewReader(pngHeaderClaiming(20000, 20000))); !errors.Is(err, ErrImageTooLarge) {
		t.Fatalf("20000x20000: err = %v, want ErrImageTooLarge", err)
	}
	if _, err := MakeThumb(bytes.NewReader(pngHeaderClaiming(12000, 10))); !errors.Is(err, ErrImageTooLarge) {
		t.Fatalf("12000 px side: err = %v, want ErrImageTooLarge", err)
	}
	// A 16-bit PNG decodes at 8 bytes per pixel: 4096x4096 would already be ~130 MB.
	if _, err := MakeThumb(bytes.NewReader(pngHeaderClaiming(4097, 100))); !errors.Is(err, ErrImageTooLarge) {
		t.Fatalf("4097 px side: err = %v, want ErrImageTooLarge", err)
	}
	if _, err := MakeThumb(bytes.NewReader(pngHeaderClaiming(4096, 4096))); !errors.Is(err, ErrImageTooLarge) {
		t.Fatalf("16.7 Mpix: err = %v, want ErrImageTooLarge", err)
	}
	big := append(pngOf(10, 10), make([]byte, maxImageBytes)...)
	if _, err := MakeThumb(bytes.NewReader(big)); !errors.Is(err, ErrImageTooLarge) {
		t.Fatalf("oversized body: err = %v, want ErrImageTooLarge", err)
	}
}
