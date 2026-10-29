package media

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// jpegSize returns the pixel size of a JPEG and fails if it is not a JPEG.
func jpegSize(t *testing.T, tools Tools, p string) (w, h int) {
	t.Helper()
	out, err := exec.Command(tools.Exiftool, "-s3", "-n", "-FileType", "-ImageWidth", "-ImageHeight", p).Output()
	if err != nil {
		t.Fatal(err)
	}
	f := strings.Fields(string(out))
	if len(f) != 3 || f[0] != "JPEG" {
		t.Fatalf("%s: exiftool = %q", p, out)
	}
	w, _ = strconv.Atoi(f[1])
	h, _ = strconv.Atoi(f[2])
	return w, h
}

func profileOf(t *testing.T, tools Tools, p string) string {
	t.Helper()
	out, _ := exec.Command(tools.Exiftool, "-s3", "-ProfileDescription", p).Output()
	return strings.TrimSpace(string(out))
}

// brightness is the mean grey level of an image on a 0..1 scale.
func brightness(t *testing.T, tools Tools, p string) float64 {
	t.Helper()
	grey := filepath.Join(t.TempDir(), "grey.v")
	run(t, tools.Vips, "colourspace", p, grey, "b-w")
	out, err := exec.Command(tools.Vips, "avg", grey).Output()
	if err != nil {
		t.Fatal(err)
	}
	v, err := strconv.ParseFloat(strings.ReplaceAll(strings.TrimSpace(string(out)), ",", "."), 64)
	if err != nil {
		t.Fatalf("avg output %q: %v", out, err)
	}
	return v / 255
}

func solidJPEG(t *testing.T, tools Tools, name, w, h string, exifArgs ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	run(t, tools.Vips, "black", p, w, h)
	if len(exifArgs) > 0 {
		run(t, tools.Exiftool, append([]string{"-overwrite_original", "-m"}, append(exifArgs, p)...)...)
	}
	return p
}

func TestThumb_SquareAndSRGB(t *testing.T) {
	tools := exifTools(t)
	src := solidJPEG(t, tools, "a.jpg", "1200", "800")
	dst := filepath.Join(t.TempDir(), "sub", "t.jpg")
	if err := Thumb(context.Background(), tools, src, Meta{Kind: "photo"}, dst); err != nil {
		t.Fatal(err)
	}
	if w, h := jpegSize(t, tools, dst); w != ThumbSize || h != ThumbSize {
		t.Fatalf("size = %dx%d", w, h)
	}
	if p := profileOf(t, tools, dst); p != "" && !strings.Contains(p, "sRGB") {
		t.Fatalf("profile = %q", p)
	}
	ents, _ := os.ReadDir(filepath.Dir(dst))
	if len(ents) != 1 {
		t.Fatalf("leftover files: %v", ents)
	}
}

func TestPreview_FitsBox(t *testing.T) {
	tools := exifTools(t)
	src := solidJPEG(t, tools, "a.jpg", "4000", "3000")
	dst := filepath.Join(t.TempDir(), "p.jpg")
	if err := Preview(context.Background(), tools, src, Meta{Kind: "photo"}, dst); err != nil {
		t.Fatal(err)
	}
	if w, h := jpegSize(t, tools, dst); w != 2048 || h != 1536 {
		t.Fatalf("size = %dx%d", w, h)
	}
}

func TestThumb_Video(t *testing.T) {
	tools := exifTools(t)
	src := filepath.Join(t.TempDir(), "v.mp4")
	run(t, tools.Ffmpeg, "-v", "error", "-f", "lavfi", "-i", "testsrc=d=2:s=320x240:r=10",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", src)
	e, _ := newExif(t)
	m, err := e.Read(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "t.jpg")
	if err := Thumb(context.Background(), tools, src, m, dst); err != nil {
		t.Fatal(err)
	}
	if w, h := jpegSize(t, tools, dst); w != ThumbSize || h != ThumbSize {
		t.Fatalf("size = %dx%d", w, h)
	}
	// A clip shorter than one second must still yield a frame.
	short := filepath.Join(t.TempDir(), "s.mp4")
	run(t, tools.Ffmpeg, "-v", "error", "-f", "lavfi", "-i", "testsrc=d=0.4:s=320x240:r=10",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", short)
	sm, _ := e.Read(context.Background(), short)
	if err := Preview(context.Background(), tools, short, sm, filepath.Join(t.TempDir(), "p.jpg")); err != nil {
		t.Fatal(err)
	}
}

func TestThumb_Orientation(t *testing.T) {
	tools := exifTools(t)
	src := solidJPEG(t, tools, "o.jpg", "600", "400", "-n", "-Orientation=6")
	dst := filepath.Join(t.TempDir(), "p.jpg")
	if err := Preview(context.Background(), tools, src, Meta{Kind: "photo", Orientation: 6}, dst); err != nil {
		t.Fatal(err)
	}
	if w, h := jpegSize(t, tools, dst); w != 400 || h != 600 {
		t.Fatalf("size = %dx%d, want portrait 400x600", w, h)
	}
}

func TestTranscodeH264(t *testing.T) {
	tools := exifTools(t)
	src := filepath.Join(t.TempDir(), "v.mov")
	run(t, tools.Ffmpeg, "-v", "error", "-f", "lavfi", "-i", "testsrc=d=1:s=2560x1440:r=10",
		"-f", "lavfi", "-i", "sine=d=1", "-shortest", "-c:v", "libx264", "-pix_fmt", "yuv420p", src)
	dst := filepath.Join(t.TempDir(), "o", "v-h264.mp4")
	if err := TranscodeH264(context.Background(), tools, src, dst); err != nil {
		t.Fatal(err)
	}
	e, _ := newExif(t)
	m, err := e.Read(context.Background(), dst)
	if err != nil {
		t.Fatal(err)
	}
	if m.VideoCodec != "h264" || m.Width != 1920 || m.Height != 1080 {
		t.Fatalf("meta = %+v", m)
	}
	// faststart: moov precedes mdat.
	b, _ := os.ReadFile(dst)
	if i, j := strings.Index(string(b), "moov"), strings.Index(string(b), "mdat"); i < 0 || j < 0 || i > j {
		t.Fatalf("moov at %d, mdat at %d", i, j)
	}
}

func TestRender_CancelledContext(t *testing.T) {
	tools := exifTools(t)
	src := solidJPEG(t, tools, "a.jpg", "100", "100")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dst := filepath.Join(t.TempDir(), "t.jpg")
	if err := Thumb(ctx, tools, src, Meta{Kind: "photo"}, dst); err == nil {
		t.Fatal("want error")
	}
	if _, err := os.Stat(dst); err == nil {
		t.Fatal("dst must not exist")
	}
}

func TestPreview_ProRAWNotDark(t *testing.T) {
	dir := os.Getenv("PHOTOS_FIXTURES")
	if dir == "" {
		t.Skip("PHOTOS_FIXTURES not set")
	}
	tools := exifTools(t)
	e, _ := newExif(t)
	ctx := context.Background()
	dng := filepath.Join(dir, "IMG_3550.DNG")
	m, err := e.Read(ctx, dng)
	if err != nil {
		t.Fatal(err)
	}
	jpg := filepath.Join(dir, "IMG_3550.jpeg")
	jm, err := e.Read(ctx, jpg)
	if err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	pd, pj := filepath.Join(out, "dng.jpg"), filepath.Join(out, "jpeg.jpg")
	if err := Preview(ctx, tools, dng, m, pd); err != nil {
		t.Fatal(err)
	}
	if err := Preview(ctx, tools, jpg, jm, pj); err != nil {
		t.Fatal(err)
	}
	bd, bj := brightness(t, tools, pd), brightness(t, tools, pj)
	t.Logf("brightness: DNG preview %.3f, JPEG preview %.3f", bd, bj)
	if d := bd - bj; d > 0.05 || d < -0.05 {
		t.Fatalf("DNG preview brightness %.3f differs from JPEG %.3f", bd, bj)
	}
	// Both are landscape after orientation; the DNG must match the JPEG's shape.
	dw, dh := jpegSize(t, tools, pd)
	jw, jh := jpegSize(t, tools, pj)
	if dw != jw || dh != jh {
		t.Fatalf("DNG preview %dx%d, JPEG preview %dx%d", dw, dh, jw, jh)
	}
	if (m.Width > m.Height) != (dw > dh) {
		t.Fatalf("orientation not applied: meta %dx%d, preview %dx%d", m.Width, m.Height, dw, dh)
	}
}

func TestPreview_RawPreviewFailureIsError(t *testing.T) {
	tools := exifTools(t)
	// A "RAW" whose preview cannot be extracted, with HasPreview claiming otherwise.
	src := filepath.Join(t.TempDir(), "x.dng")
	if err := os.WriteFile(src, []byte("not a raw"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "p.jpg")
	if err := Preview(context.Background(), tools, src, Meta{Kind: "photo", HasPreview: true}, dst); err == nil {
		t.Fatal("want error when the embedded preview cannot be extracted")
	}
	if _, err := os.Stat(dst); err == nil {
		t.Fatal("dst must not exist")
	}
	// A failing exiftool binary behaves the same.
	fake := filepath.Join(t.TempDir(), "exiftool")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	ft := tools
	ft.Exiftool = fake
	if err := Preview(context.Background(), ft, src, Meta{Kind: "photo", HasPreview: true}, dst); err == nil {
		t.Fatal("want error with failing exiftool")
	}
}

func TestTranscodeH264_Portrait(t *testing.T) {
	tools := exifTools(t)
	e, _ := newExif(t)
	for _, c := range []struct{ in, w, h string }{
		{"1080x1920", "1080", "1920"},
		{"2160x3840", "1080", "1920"},
		{"640x360", "640", "360"},
	} {
		src := filepath.Join(t.TempDir(), "v.mp4")
		run(t, tools.Ffmpeg, "-v", "error", "-f", "lavfi", "-i", "testsrc=d=1:s="+c.in+":r=5",
			"-c:v", "libx264", "-pix_fmt", "yuv420p", src)
		dst := filepath.Join(t.TempDir(), "o.mp4")
		if err := TranscodeH264(context.Background(), tools, src, dst); err != nil {
			t.Fatal(err)
		}
		m, err := e.Read(context.Background(), dst)
		if err != nil {
			t.Fatal(err)
		}
		if strconv.Itoa(m.Width) != c.w || strconv.Itoa(m.Height) != c.h {
			t.Errorf("%s -> %dx%d, want %sx%s", c.in, m.Width, m.Height, c.w, c.h)
		}
	}
}

// strippedDNG copies the ProRAW fixture into a temp dir without its big preview and puts
// a small colour one in its place, the way an old DNG has it. Only the grey semantic
// masks (OtherImage) stay besides.
func strippedDNG(t *testing.T, tools Tools) string {
	t.Helper()
	p := masksOnlyDNG(t, tools)
	small := filepath.Join(t.TempDir(), "small.jpg")
	run(t, tools.Vips, "thumbnail", filepath.Join(os.Getenv("PHOTOS_FIXTURES"), "IMG_3550.jpeg"), small, "320")
	run(t, tools.Exiftool, "-q", "-overwrite_original", "-PreviewImage<="+small, p)
	return p
}

// masksOnlyDNG is the ProRAW fixture with no preview at all, only the semantic masks.
func masksOnlyDNG(t *testing.T, tools Tools) string {
	t.Helper()
	dir := os.Getenv("PHOTOS_FIXTURES")
	if dir == "" {
		t.Skip("PHOTOS_FIXTURES not set")
	}
	b, err := os.ReadFile(filepath.Join(dir, "IMG_3550.DNG"))
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "old.dng")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	run(t, tools.Exiftool, "-q", "-overwrite_original", "-PreviewImage=", p)
	return p
}

// A DNG without a big embedded preview is developed with its BaselineExposure: not the
// dark picture a plain decode gives, and the right way up.
func TestPreview_RAWWithoutPreviewDeveloped(t *testing.T) {
	tools := exifTools(t)
	e, _ := newExif(t)
	ctx := context.Background()
	dng := strippedDNG(t, tools)
	m, err := e.Read(ctx, dng)
	if err != nil {
		t.Fatal(err)
	}
	if m.HasPreview || m.BaselineExposure < 4 {
		t.Fatalf("fixture: HasPreview %v BaselineExposure %v", m.HasPreview, m.BaselineExposure)
	}
	jpg := filepath.Join(os.Getenv("PHOTOS_FIXTURES"), "IMG_3550.jpeg")
	jm, err := e.Read(ctx, jpg)
	if err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	pd, pj, plain := filepath.Join(out, "dng.jpg"), filepath.Join(out, "jpeg.jpg"), filepath.Join(out, "plain.jpg")
	if err := Preview(ctx, tools, dng, m, pd); err != nil {
		t.Skipf("this vips cannot develop raws: %v", err)
	}
	if err := Preview(ctx, tools, jpg, jm, pj); err != nil {
		t.Fatal(err)
	}
	flat := m
	flat.BaselineExposure = 0
	if err := Preview(ctx, tools, dng, flat, plain); err != nil {
		t.Fatal(err)
	}
	bd, bj, bp := brightness(t, tools, pd), brightness(t, tools, pj), brightness(t, tools, plain)
	t.Logf("brightness: developed %.3f, plain decode %.3f, Apple JPEG %.3f", bd, bp, bj)
	if bd < bj*0.8 || bd > bj*1.4 {
		t.Fatalf("developed DNG brightness %.3f, Apple JPEG %.3f", bd, bj)
	}
	if bd <= bp+0.1 {
		t.Fatalf("BaselineExposure not applied: %.3f vs plain %.3f", bd, bp)
	}
	dw, dh := jpegSize(t, tools, pd)
	if (m.Width > m.Height) != (dw > dh) {
		t.Fatalf("orientation: meta %dx%d, preview %dx%d", m.Width, m.Height, dw, dh)
	}
}

// Where the raw cannot be developed (no raw loader in vips), the largest embedded
// preview is used, however small.
func TestRawFallback_SmallPreview(t *testing.T) {
	tools := exifTools(t)
	e, _ := newExif(t)
	ctx := context.Background()
	dng := strippedDNG(t, tools)
	m, err := e.Read(ctx, dng)
	if err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(t.TempDir(), "vips")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho 'no raw loader' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	ft := tools
	ft.Vips = fake
	p, err := rawFallback(ctx, ft, dng, m, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if w, h := jpegSize(t, tools, p); w == 0 || h == 0 {
		t.Fatalf("embedded preview %dx%d", w, h)
	}
	if st, err := os.Stat(p); err != nil || st.Size() >= rawPreviewMinBytes {
		t.Fatalf("want the small embedded preview: %v %v", st, err)
	}
	// And the whole render goes through with it.
	dst := filepath.Join(t.TempDir(), "t.jpg")
	if err := Thumb(ctx, tools, dng, Meta{Kind: "photo", Orientation: m.Orientation}, dst); err != nil {
		t.Fatal(err)
	}
}

// A vips without libraw opens a DNG as a TIFF and "succeeds" with the wrong picture, so
// it must not be asked to develop at all: the embedded preview is used straight away.
func TestRawFallback_VipsWithoutRawLoader(t *testing.T) {
	tools := exifTools(t)
	e, _ := newExif(t)
	ctx := context.Background()
	dng := strippedDNG(t, tools)
	m, err := e.Read(ctx, dng)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "developed")
	fake := filepath.Join(dir, "vips")
	script := "#!/bin/sh\nif [ \"$1\" = -l ]; then echo 'VipsForeignLoadTiffFile (tiffload)'; exit 0; fi\ntouch " + marker + "\nexit 1\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	ft := tools
	ft.Vips = fake
	p, err := rawFallback(ctx, ft, dng, m, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("vips without a raw loader was asked to develop the DNG")
	}
	if w, h := jpegSize(t, tools, p); w == 0 || h == 0 {
		t.Fatalf("embedded preview %dx%d", w, h)
	}
}

// A semantic mask is not a picture of the scene: a DNG with nothing else in it and no
// raw loader fails to render instead of showing a grey mask.
func TestRawFallback_MasksAreNotPreviews(t *testing.T) {
	tools := exifTools(t)
	e, _ := newExif(t)
	ctx := context.Background()
	dng := masksOnlyDNG(t, tools)
	m, err := e.Read(ctx, dng)
	if err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(t.TempDir(), "vips")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	ft := tools
	ft.Vips = fake
	if p, err := rawFallback(ctx, ft, dng, m, t.TempDir()); err == nil {
		t.Fatalf("rendered %s from a DNG that has only masks", p)
	}
}

// Pixel Motion Photos (.MP.mp4) carry a second, larger video stream with a single frame
// at the start; ffmpeg picks the largest stream by default, which has nothing past 0 s.
// Clips of one or two frames have nothing at the seek point either.
func TestThumb_VideoFrameOfMainStream(t *testing.T) {
	tools := exifTools(t)
	e, _ := newExif(t)
	ctx := context.Background()
	dir := t.TempDir()
	motion := filepath.Join(dir, "PXL.MP.mp4")
	run(t, tools.Ffmpeg, "-v", "error", "-f", "lavfi", "-i", "testsrc=d=0.8:s=320x240:r=30",
		"-f", "lavfi", "-i", "color=c=red:s=640x480:r=25:d=0.04",
		"-map", "0:v", "-map", "1:v", "-c:v", "libx264", "-pix_fmt", "yuv420p", motion)
	tiny := filepath.Join(dir, "tiny.mov")
	run(t, tools.Ffmpeg, "-v", "error", "-f", "lavfi", "-i", "testsrc=d=0.04:s=320x240:r=25",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", tiny)
	for _, src := range []string{motion, tiny} {
		m, err := e.Read(ctx, src)
		if err != nil {
			t.Fatal(err)
		}
		dst := filepath.Join(t.TempDir(), "p.jpg")
		if err := Preview(ctx, tools, src, m, dst); err != nil {
			t.Fatalf("%s: %v", filepath.Base(src), err)
		}
		if w, h := jpegSize(t, tools, dst); w != 320 || h != 240 {
			t.Fatalf("%s: frame %dx%d, want the main stream's 320x240", filepath.Base(src), w, h)
		}
	}
}

// Some apps save a plain JPEG under a .dng name: it is shown as the JPEG it is.
func TestPreview_JPEGNamedDNG(t *testing.T) {
	tools := exifTools(t)
	e, _ := newExif(t)
	ctx := context.Background()
	src := filepath.Join(t.TempDir(), "IMG_3588.DNG")
	if err := os.Rename(solidJPEG(t, tools, "a.jpg", "1506", "2008", "-Make=Apple"), src); err != nil {
		t.Fatal(err)
	}
	m, err := e.Read(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "p.jpg")
	if err := Preview(ctx, tools, src, m, dst); err != nil {
		t.Fatal(err)
	}
	if w, h := jpegSize(t, tools, dst); w != 1506 || h != 2008 {
		t.Fatalf("size = %dx%d", w, h)
	}
}

// A truncated HEIC (an interrupted copy) cannot be decoded; its embedded thumbnail shows
// the whole picture, small.
func TestRender_TruncatedHEICUsesEmbeddedThumbnail(t *testing.T) {
	tools := exifTools(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "IMG_7840.HEIC")
	run(t, tools.Vips, "black", filepath.Join(dir, "b.v"), "320", "240")
	run(t, tools.Vips, "copy", filepath.Join(dir, "b.v"), src)
	// vips that fails on the image itself, like libheif on a cut-off file; `vips
	// thumbnail` re-opens the full image even when asked for the [thumbnail] one.
	fake := filepath.Join(dir, "vips")
	script := "#!/bin/sh\ncase \"$1 $2\" in \"thumbnail \"*.HEIC*) echo 'error in tile 0 x 8' >&2; exit 1;; esac\nexec " + tools.Vips + " \"$@\"\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	ft := tools
	ft.Vips = fake
	dst := filepath.Join(t.TempDir(), "t.jpg")
	if err := Thumb(context.Background(), ft, src, Meta{Kind: "photo"}, dst); err != nil {
		t.Fatal(err)
	}
	if w, h := jpegSize(t, tools, dst); w != ThumbSize || h != ThumbSize {
		t.Fatalf("size = %dx%d", w, h)
	}
}
