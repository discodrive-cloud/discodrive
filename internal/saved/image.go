package saved

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// MaxImageBytes caps one proxied article image. Real photos in articles are far
// smaller; the cap bounds what one request can make the server download.
const MaxImageBytes = 8 << 20

var (
	// ErrNotImage: the upstream body is not one of the raster formats below.
	ErrNotImage = errors.New("saved: not a raster image")
	// ErrImageTooLarge: the upstream announced more than MaxImageBytes.
	ErrImageTooLarge = errors.New("saved: image too large")
	// ErrImageUpstream: the upstream answered with an error status.
	ErrImageUpstream = errors.New("saved: image upstream error")
)

// Image is an opened, sniffed article image. Body yields at most MaxImageBytes;
// the caller closes it.
type Image struct {
	ContentType string
	Size        int64 // -1 when the upstream did not say
	Body        io.ReadCloser
}

// OpenImage fetches an image referenced by a saved article through the SSRF-guarded
// client (Validate, then the dial-time check and per-redirect revalidation of Client).
// The type comes from sniffing the first bytes, never from the upstream header, and
// only raster formats pass: SVG can carry script, and an HTML page served as
// "image/png" must not come back as anything a browser would render.
func (s *Service) OpenImage(ctx context.Context, rawURL string) (*Image, error) {
	if err := s.Validate(rawURL); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "image/avif,image/webp,image/png,image/jpeg,image/gif;q=0.9,*/*;q=0.1")
	resp, err := s.Client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("%w: status %d", ErrImageUpstream, resp.StatusCode)
	}
	if resp.ContentLength > MaxImageBytes {
		resp.Body.Close()
		return nil, ErrImageTooLarge
	}
	head := make([]byte, 512)
	n, err := io.ReadFull(resp.Body, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		resp.Body.Close()
		return nil, err
	}
	head = head[:n]
	ct := sniffRaster(head)
	if ct == "" {
		resp.Body.Close()
		return nil, ErrNotImage
	}
	body := io.MultiReader(bytes.NewReader(head), io.LimitReader(resp.Body, MaxImageBytes-int64(n)))
	return &Image{ContentType: ct, Size: resp.ContentLength, Body: readCloser{body, resp.Body}}, nil
}

type readCloser struct {
	io.Reader
	io.Closer
}

// sniffRaster returns the content type of a PNG, JPEG, GIF, WebP, BMP or AVIF image,
// or "" for anything else.
func sniffRaster(head []byte) string {
	switch ct := http.DetectContentType(head); ct {
	case "image/png", "image/jpeg", "image/gif", "image/webp", "image/bmp":
		return ct
	}
	// ISO-BMFF "ftyp" box with an AVIF brand (net/http does not sniff AVIF).
	if len(head) >= 12 && string(head[4:8]) == "ftyp" {
		if brand := string(head[8:12]); brand == "avif" || brand == "avis" {
			return "image/avif"
		}
	}
	return ""
}
