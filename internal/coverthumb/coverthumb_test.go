package coverthumb

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"
)

func picture(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		img.Set(x, x%h, color.RGBA{200, 30, 30, 255})
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

const key = "5226a1ddeecf8587f1ab83f254e44c5d64ccb55a95ab1d911ef76b6838bf6b41"

func TestSideServesTheSmallestKeptSizeThatCovers(t *testing.T) {
	for asked, want := range map[int]int{0: 0, -5: 0, 1: 160, 160: 160, 161: 320, 300: 320, 600: 640, 1000: 1200, 5000: 0} {
		if got := Side(asked); got != want {
			t.Errorf("Side(%d) = %d, want %d", asked, got, want)
		}
	}
}

func TestGetScalesOnceAndThenServesFromDisk(t *testing.T) {
	root := t.TempDir()
	calls := 0
	original := func() ([]byte, bool) { calls++; return picture(t, 1500, 1000), true }

	first, ok := Get(root, key, 320, original)
	if !ok {
		t.Fatal("no thumbnail for a decodable picture")
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(first))
	if err != nil {
		t.Fatalf("the thumbnail is not a JPEG: %v", err)
	}
	if cfg.Width != 320 || cfg.Height > 320 {
		t.Errorf("thumbnail is %dx%d, want 320 on the long side", cfg.Width, cfg.Height)
	}
	if len(first) >= len(picture(t, 1500, 1000)) {
		t.Errorf("the thumbnail (%d bytes) is not smaller than the original", len(first))
	}

	second, ok := Get(root, key, 320, original)
	if !ok || !bytes.Equal(first, second) {
		t.Fatal("the second request did not get the same thumbnail")
	}
	if calls != 1 {
		t.Errorf("the original picture was read %d times, want once", calls)
	}
	// Another size of the same picture is its own entry.
	if _, ok := Get(root, key, 160, original); !ok || calls != 2 {
		t.Errorf("a second size: ok=%v, original read %d times", ok, calls)
	}
}

func TestGetGivesNothingItCannotStandBehind(t *testing.T) {
	root := t.TempDir()
	never := func() ([]byte, bool) { t.Fatal("the original was read"); return nil, false }
	// A key is a path component: anything but a hash is refused before the disk is touched.
	for _, bad := range []string{"", "../../etc/passwd", strings.Repeat("Z", 64), "abc"} {
		if _, ok := Get(root, bad, 320, never); ok {
			t.Errorf("key %q was accepted", bad)
		}
	}
	// Only the kept sizes exist.
	if _, ok := Get(root, key, 317, never); ok {
		t.Error("an arbitrary size was accepted")
	}
	if _, ok := Get(root, key, 320, func() ([]byte, bool) { return nil, false }); ok {
		t.Error("a thumbnail of a missing picture")
	}
	if _, ok := Get(root, key, 320, func() ([]byte, bool) { return []byte("<svg></svg>"), true }); ok {
		t.Error("a thumbnail of something that is not a raster image")
	}
}
