package links

import (
	"bytes"
	"errors"
	"image"
	_ "image/gif" // decoders registered for image.Decode
	"image/jpeg"
	_ "image/png"
	"io"
	"net/http"

	_ "golang.org/x/image/bmp"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

var (
	ErrNotImage      = errors.New("links: not a supported image")
	ErrImageTooLarge = errors.New("links: image too large")
)

// Limits on a preview image. They are checked before anything is decoded: the header
// is read first, so a tiny file declaring a gigantic canvas costs nothing.
const (
	maxImageBytes = 5 << 20
	// A 16-bit PNG decodes at 8 bytes per pixel and a progressive JPEG keeps ~12 B/px of
	// coefficients: 16 Mpix is ~200 MB at worst. Previews are 1–3 Mpix in practice.
	maxImagePixels = 16_000_000
	maxImageSide   = 4096
	thumbSide      = 160
	thumbQuality   = 80
)

var decodeSlot = make(chan struct{}, 1)

// MakeThumb turns an untrusted image into a small JPEG. The type is sniffed from the
// bytes (never the upstream header) and only raster formats pass, SVG never. The
// result is a fresh encoding of the decoded pixels: nothing of the original file
// (metadata, trailing payloads, polyglot tricks) survives into what is stored and served.
func MakeThumb(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxImageBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxImageBytes {
		return nil, ErrImageTooLarge
	}
	switch http.DetectContentType(data) {
	case "image/png", "image/jpeg", "image/gif", "image/webp", "image/bmp":
	default:
		return nil, ErrNotImage
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, ErrNotImage
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return nil, ErrNotImage
	}
	if cfg.Width > maxImageSide || cfg.Height > maxImageSide || cfg.Width*cfg.Height > maxImagePixels {
		return nil, ErrImageTooLarge
	}
	// One decode at a time server-wide: four fetches in parallel must not mean four
	// worst-case decodes in memory at once.
	// The slot is held until the decoded image is scaled down and can be dropped.
	decodeSlot <- struct{}{}
	defer func() { <-decodeSlot }()
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, ErrNotImage
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w > thumbSide || h > thumbSide {
		if w >= h {
			w, h = thumbSide, max(1, h*thumbSide/w)
		} else {
			w, h = max(1, w*thumbSide/h), thumbSide
		}
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	// JPEG has no alpha: paint transparent areas white rather than black.
	draw.Draw(dst, dst.Bounds(), image.White, image.Point{}, draw.Src)
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Over, nil)
	var out bytes.Buffer
	if err := jpeg.Encode(&out, dst, &jpeg.Options{Quality: thumbQuality}); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
