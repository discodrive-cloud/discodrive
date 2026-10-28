package media

import (
	"context"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func exifTools(t *testing.T) Tools {
	t.Helper()
	tools := Detect()
	if !tools.Available() {
		t.Skip("media tools not available")
	}
	return tools
}

func newExif(t *testing.T) (*Exif, Tools) {
	t.Helper()
	tools := exifTools(t)
	e, err := NewExif(tools)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	return e, tools
}

func run(t *testing.T, name string, args ...string) {
	t.Helper()
	if out, err := exec.Command(name, args...).CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
}

// makeJPEG writes a small synthetic JPEG and applies extra exiftool args.
func makeJPEG(t *testing.T, tools Tools, name string, exifArgs ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	run(t, tools.Vips, "black", p, "64", "48")
	if len(exifArgs) > 0 {
		run(t, tools.Exiftool, append([]string{"-overwrite_original", "-m"}, append(exifArgs, p)...)...)
	}
	return p
}

func TestRead_JPEGDateOffsetGPS(t *testing.T) {
	e, tools := newExif(t)
	p := makeJPEG(t, tools, "a.jpg",
		"-DateTimeOriginal=2019:07:14 19:42:00", "-OffsetTimeOriginal=+06:00",
		"-GPSLatitude=42.6475", "-GPSLatitudeRef=N", "-GPSLongitude=77.0820", "-GPSLongitudeRef=E",
		"-Make=Acme", "-Model=Cam 1", "-ISO=200", "-ExposureTime=0.0125", "-FNumber=1.8", "-FocalLength=26")
	m, err := e.Read(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2019, 7, 14, 13, 42, 0, 0, time.UTC)
	if m.TakenAt == nil || !m.TakenAt.Equal(want) {
		t.Fatalf("TakenAt = %v, want %v", m.TakenAt, want)
	}
	if m.TZOffsetMin == nil || *m.TZOffsetMin != 360 {
		t.Fatalf("TZOffsetMin = %v, want 360", m.TZOffsetMin)
	}
	if m.Lat == nil || math.Abs(*m.Lat-42.6475) > 1e-4 || m.Lon == nil || math.Abs(*m.Lon-77.0820) > 1e-4 {
		t.Fatalf("lat/lon = %v/%v", m.Lat, m.Lon)
	}
	if m.Kind != "photo" || m.Make != "Acme" || m.Model != "Cam 1" || m.ISO != 200 || m.ExposureS != "1/80" {
		t.Fatalf("meta = %+v", m)
	}
	if m.Width != 64 || m.Height != 48 {
		t.Fatalf("size = %dx%d", m.Width, m.Height)
	}
}

func TestRead_NoOffsetKeepsWallClock(t *testing.T) {
	e, tools := newExif(t)
	p := makeJPEG(t, tools, "a.jpg", "-DateTimeOriginal=2019:07:14 19:42:00")
	m, err := e.Read(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if m.TakenAt == nil || !m.TakenAt.Equal(time.Date(2019, 7, 14, 19, 42, 0, 0, time.UTC)) || m.TZOffsetMin != nil {
		t.Fatalf("TakenAt=%v off=%v", m.TakenAt, m.TZOffsetMin)
	}
}

func TestRead_ZeroDateAndZeroGPS(t *testing.T) {
	e, tools := newExif(t)
	p := makeJPEG(t, tools, "z.jpg", "-DateTimeOriginal=0000:00:00 00:00:00",
		"-GPSLatitude=0", "-GPSLatitudeRef=N", "-GPSLongitude=0", "-GPSLongitudeRef=E")
	m, err := e.Read(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if m.TakenAt != nil || m.Lat != nil || m.Lon != nil {
		t.Fatalf("want no date and no coords, got %+v", m)
	}
}

func TestRead_TruncatedFile(t *testing.T) {
	e, tools := newExif(t)
	p := makeJPEG(t, tools, "t.jpg", "-DateTimeOriginal=2019:07:14 19:42:00")
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	half := len(data) / 2
	broken := append(append([]byte{}, data[:half]...), make([]byte, len(data)-half)...)
	if err := os.WriteFile(p, broken, 0o644); err != nil {
		t.Fatal(err)
	}
	m, _ := e.Read(context.Background(), p) // error or not, but no panic
	if m.Kind != "photo" {
		t.Fatalf("Kind = %q", m.Kind)
	}
}

func TestRead_XMPSidecarOverrides(t *testing.T) {
	e, tools := newExif(t)
	p := makeJPEG(t, tools, "s.jpg", "-DateTimeOriginal=2019:07:14 19:42:00", "-OffsetTimeOriginal=+06:00",
		"-GPSLatitude=42.6475", "-GPSLatitudeRef=N", "-GPSLongitude=77.0820", "-GPSLongitudeRef=E")
	run(t, tools.Exiftool, "-o", p+".xmp", "-XMP-exif:DateTimeOriginal=2020-01-02T03:04:05+03:00",
		"-XMP-exif:GPSLatitude=10.5", "-XMP-exif:GPSLongitude=20.25")
	m, err := e.Read(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2020, 1, 2, 0, 4, 5, 0, time.UTC)
	if m.TakenAt == nil || !m.TakenAt.Equal(want) {
		t.Fatalf("TakenAt = %v, want %v", m.TakenAt, want)
	}
	if m.TZOffsetMin == nil || *m.TZOffsetMin != 180 {
		t.Fatalf("TZOffsetMin = %v", m.TZOffsetMin)
	}
	if m.Lat == nil || math.Abs(*m.Lat-10.5) > 1e-4 || m.Lon == nil || math.Abs(*m.Lon-20.25) > 1e-4 {
		t.Fatalf("lat/lon = %v/%v", m.Lat, m.Lon)
	}
}

func TestRead_VideoDurationCodec(t *testing.T) {
	e, tools := newExif(t)
	p := filepath.Join(t.TempDir(), "v.mp4")
	run(t, tools.Ffmpeg, "-v", "error", "-f", "lavfi", "-i", "testsrc=d=2:s=160x120:r=10",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", p)
	m, err := e.Read(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if m.Kind != "video" || m.VideoCodec != "h264" {
		t.Fatalf("meta = %+v", m)
	}
	if m.DurationMS < 1900 || m.DurationMS > 2100 {
		t.Fatalf("DurationMS = %d", m.DurationMS)
	}
	if m.Width != 160 || m.Height != 120 {
		t.Fatalf("size = %dx%d", m.Width, m.Height)
	}
}

func TestRead_CancelAndCrashRecovery(t *testing.T) {
	e, tools := newExif(t)
	p := makeJPEG(t, tools, "c.jpg", "-DateTimeOriginal=2019:07:14 19:42:00")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.Read(ctx, p); err == nil {
		t.Fatal("want error from cancelled context")
	}

	// Kill the process behind the reader's back: the next Read must restart it.
	e.mu.Lock()
	_ = e.cmd.Process.Kill()
	e.mu.Unlock()
	time.Sleep(100 * time.Millisecond)
	for i := 0; i < 3; i++ {
		m, err := e.Read(context.Background(), p)
		if err != nil || m.TakenAt == nil {
			t.Fatalf("read %d after crash: %+v, %v", i, m, err)
		}
	}
}

func TestParseDate(t *testing.T) {
	for _, s := range []string{"0000:00:00 00:00:00", "1850:01:01 00:00:00", "2020:02:31 10:00:00", "junk", ""} {
		if _, _, ok := parseDate(s); ok {
			t.Errorf("parseDate(%q) accepted", s)
		}
	}
	tm, off, ok := parseDate("2023:05:01 12:00:00.123-05:30")
	if !ok || off == nil || *off != -330 || !tm.Equal(time.Date(2023, 5, 1, 17, 30, 0, 0, time.UTC)) {
		t.Errorf("got %v %v %v", tm, off, ok)
	}
}

func TestRead_ProRAWFixture(t *testing.T) {
	dir := os.Getenv("PHOTOS_FIXTURES")
	if dir == "" {
		t.Skip("PHOTOS_FIXTURES not set")
	}
	e, _ := newExif(t)
	m, err := e.Read(context.Background(), filepath.Join(dir, "IMG_3550.DNG"))
	if err != nil {
		t.Fatal(err)
	}
	if !m.HasPreview || m.Model != "iPhone 17 Pro" {
		t.Fatalf("meta = %+v", m)
	}
	if m.Orientation != 6 || m.Width != 4896 || m.Height != 3672 {
		t.Fatalf("orientation/size = %d %dx%d", m.Orientation, m.Width, m.Height)
	}
	if !m.HDR {
		t.Errorf("HDR = false, want true (Apple HDRGain)")
	}
}

func TestRead_NewlineInPathIsNotInjected(t *testing.T) {
	e, tools := newExif(t)
	src := makeJPEG(t, tools, "src.jpg", "-DateTimeOriginal=2019:07:14 19:42:00")
	dir := t.TempDir()
	evil := filepath.Join(dir, "a\n-o\nx.jpg")
	if err := os.Rename(src, evil); err != nil {
		t.Skipf("filesystem refuses newline in name: %v", err)
	}
	m, err := e.Read(context.Background(), evil)
	if err != nil {
		t.Fatal(err)
	}
	if m.TakenAt == nil || !m.TakenAt.Equal(time.Date(2019, 7, 14, 19, 42, 0, 0, time.UTC)) {
		t.Fatalf("TakenAt = %v", m.TakenAt)
	}
	// "-o x.jpg" must not have taken effect, and the reader stays in sync.
	if _, err := os.Stat("x.jpg"); err == nil {
		t.Fatal("injected -o argument took effect")
	}
	if _, err := os.Stat(filepath.Join(dir, "x.jpg")); err == nil {
		t.Fatal("injected -o argument took effect")
	}
	if _, err := e.Read(context.Background(), evil); err != nil {
		t.Fatal(err)
	}
}

func TestRead_VideoRotation(t *testing.T) {
	e, tools := newExif(t)
	d := t.TempDir()
	in := filepath.Join(d, "in.mp4")
	out := filepath.Join(d, "rot.mp4")
	run(t, tools.Ffmpeg, "-v", "error", "-f", "lavfi", "-i", "testsrc=d=1:s=160x120:r=10",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", in)
	run(t, tools.Ffmpeg, "-v", "error", "-display_rotation", "90", "-i", in, "-c", "copy", out)
	m, err := e.Read(context.Background(), out)
	if err != nil {
		t.Fatal(err)
	}
	if m.Width != 120 || m.Height != 160 {
		t.Fatalf("size = %dx%d, want 120x160", m.Width, m.Height)
	}
}

// A file exiftool never answers for times out, and the reads after it work (the process
// is replaced): one bad file cannot stop the rest.
func TestExif_ReadTimeout(t *testing.T) {
	fake := filepath.Join(t.TempDir(), "exiftool")
	script := `#!/bin/sh
hang=0
while IFS= read -r line; do
  case "$line" in
    *hang*) hang=1 ;;
    -execute)
      if [ "$hang" = 1 ]; then sleep 30; fi
      printf '[{"SourceFile":"x","File:FileType":"JPEG","EXIF:Make":"Fake"}]\n{ready}\n'
      hang=0 ;;
  esac
done
`
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	e, err := NewExif(Tools{Exiftool: fake})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	e.Timeout = 2 * time.Second // room for a loaded machine; the stuck read sleeps 30 s
	ctx := context.Background()

	if m, err := e.Read(ctx, "/nowhere/ok.jpg"); err != nil || m.Make != "Fake" {
		t.Fatalf("first read: %+v %v", m, err)
	}
	start := time.Now()
	m, err := e.Read(ctx, "/nowhere/hang.jpg")
	if !errors.Is(err, ErrExifTimeout) {
		t.Fatalf("stuck read: %v, want ErrExifTimeout", err)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("stuck read took %s", d)
	}
	if m.Kind != "photo" {
		t.Fatalf("a timed-out read still reports the kind: %+v", m)
	}
	for i := 0; i < 2; i++ {
		if m, err := e.Read(ctx, "/nowhere/ok.jpg"); err != nil || m.Make != "Fake" {
			t.Fatalf("read %d after the timeout: %+v %v", i, m, err)
		}
	}
}
