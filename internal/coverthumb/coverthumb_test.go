package coverthumb

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"strings"
	"sync"
	"testing"
	"time"
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

// Scaling decodes the whole picture into memory. A cover beyond what a small machine can
// afford is not decoded: the caller serves the file itself, as it did before thumbnails.
func TestGetLeavesAHugeCoverAlone(t *testing.T) {
	root := t.TempDir()
	if _, ok := Get(root, key, 320, func() ([]byte, bool) { return picture(t, 2400, 2400), true }); ok {
		t.Fatal("a 5.8-megapixel cover was decoded")
	}
	if _, ok := Get(root, key, 320, func() ([]byte, bool) { return picture(t, 2000, 2000), true }); !ok {
		t.Fatal("a 2000x2000 cover got no thumbnail")
	}
}

// A cold grid asks for the same cover from several places at once, and for a hundred
// others. One picture is in memory at a time, and a picture is made once.
func TestGetMakesOneThumbnailAtATime(t *testing.T) {
	root := t.TempDir()
	data := picture(t, 600, 600)
	var mu sync.Mutex
	inside, most, calls := 0, 0, 0
	original := func() ([]byte, bool) {
		mu.Lock()
		inside++
		calls++
		most = max(most, inside)
		mu.Unlock()
		time.Sleep(20 * time.Millisecond)
		mu.Lock()
		inside--
		mu.Unlock()
		return data, true
	}
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			k := key
			if i >= 3 {
				k = strings.Repeat("ab", 32)
			}
			if _, ok := Get(root, k, 320, original); !ok {
				t.Error("no thumbnail")
			}
		}(i)
	}
	wg.Wait()
	if most != 1 {
		t.Errorf("%d originals were held at once, want 1", most)
	}
	if calls != 2 {
		t.Errorf("the originals were read %d times for 2 pictures", calls)
	}
}

// The line for the one slot is short: past it a request gets no thumbnail right away
// (and so the original, as before) instead of waiting behind a whole cold grid.
func TestGetDoesNotQueueWithoutBound(t *testing.T) {
	root := t.TempDir()
	data := picture(t, 600, 600)
	started, release := make(chan struct{}), make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		Get(root, key, 320, func() ([]byte, bool) { close(started); <-release; return data, true })
	}()
	<-started
	keyOf := func(i int) string { return fmt.Sprintf("%064x", i+1) }
	for i := 0; i < maxWaiting; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			Get(root, keyOf(i), 320, func() ([]byte, bool) { return data, true })
		}(i)
	}
	deadline := time.Now().Add(5 * time.Second)
	for waiting.Load() < maxWaiting {
		if time.Now().After(deadline) {
			t.Fatalf("only %d requests lined up", waiting.Load())
		}
		time.Sleep(time.Millisecond)
	}
	if _, ok := Get(root, keyOf(1000), 320, func() ([]byte, bool) { t.Error("the original was read"); return nil, false }); ok {
		t.Error("a request past the line got a thumbnail")
	}
	close(release)
	wg.Wait()
}
