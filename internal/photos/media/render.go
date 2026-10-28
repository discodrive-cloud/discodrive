package media

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image/color"
	"image/jpeg"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

const (
	// ThumbSize is the side of the square, centre-cropped grid thumbnail.
	ThumbSize = 400
	// PreviewSize is the box (both sides) the viewer preview is fitted into.
	PreviewSize = 2048
)

// Thumb renders a ThumbSize square JPEG (centre crop, sRGB) of src into dst.
func Thumb(ctx context.Context, t Tools, src string, m Meta, dst string) error {
	return renderImage(ctx, t, src, m, dst, ThumbSize, true)
}

// Preview renders a JPEG fitted into PreviewSize x PreviewSize (sRGB) into dst.
func Preview(ctx context.Context, t Tools, src string, m Meta, dst string) error {
	return renderImage(ctx, t, src, m, dst, PreviewSize, false)
}

func renderImage(ctx context.Context, t Tools, src string, m Meta, dst string, size int, crop bool) error {
	if t.Vips == "" {
		return errors.New("vips not found")
	}
	work, err := os.MkdirTemp("", "render-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)

	in, err := imageSource(ctx, t, src, m, work)
	if err != nil {
		return err
	}
	return atomicWrite(dst, func(tmp string) error {
		s := strconv.Itoa(size)
		args := []string{"thumbnail", in, tmp + "[Q=82,strip]", s, "--height", s, "--export-profile", "srgb"}
		if crop {
			args = append(args, "--crop", "centre")
		} else {
			// A preview never enlarges a small original.
			args = append(args, "--size", "down")
		}
		return runCmd(ctx, t.Vips, args...)
	})
}

// Resource limits of one render. The gallery renders in the background next to a file
// server (the first run over a big archive takes hours), so each external process gets
// a small share of the machine instead of a thread per core; parallelism comes from the
// number of workers (PHOTOS_WORKERS) instead.
//   - vips: VIPS_CONCURRENCY threads (default 1; a VIPS_CONCURRENCY set for the server
//     is passed through as it is). `vips thumbnail` already decodes with sequential
//     access and shrink-on-load, which keeps its memory low.
//   - ffmpeg: PHOTOS_FFMPEG_THREADS threads (default 2) for decoding and libx264.
const (
	defaultVipsConcurrency = "1"
	defaultFfmpegThreads   = "2"
)

// toolEnv is the environment of a vips run: the server's, with the thread limit added
// unless the server sets one itself.
func toolEnv() []string {
	env := os.Environ()
	if os.Getenv("VIPS_CONCURRENCY") == "" {
		env = append(env, "VIPS_CONCURRENCY="+defaultVipsConcurrency)
	}
	return env
}

// ffmpegThreads is the -threads value of every ffmpeg run.
func ffmpegThreads() string {
	if v := os.Getenv("PHOTOS_FFMPEG_THREADS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return strconv.Itoa(n)
		}
	}
	return defaultFfmpegThreads
}

// imageSource returns a path vips can read for the picture to be shown: the
// embedded preview of a RAW, a poster frame of a video, or src itself.
// Temporary files are created in work.
func imageSource(ctx context.Context, t Tools, src string, m Meta, work string) (string, error) {
	abs, err := filepath.Abs(src)
	if err != nil {
		return "", err
	}
	switch {
	case m.Kind == "video":
		return videoFrame(ctx, t, abs, m, work)
	case rawExts[strings.ToLower(filepath.Ext(abs))] && m.HasPreview:
		// Developing the raw ourselves ignores BaselineExposure and gain tables
		// (Apple ProRAW comes out about twice too dark); the preview rendered by
		// the camera is what the user expects to see.
		// No silent fallback: decoding the raw directly would yield a dark
		// picture, so a failed extraction must surface as an error.
		p, err := rawPreview(ctx, t, abs, m, work, rawPreviewMinBytes)
		if err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			return "", fmt.Errorf("raw preview: %w", err)
		}
		return p, nil
	case rawExts[strings.ToLower(filepath.Ext(abs))]:
		return rawFallback(ctx, t, abs, m, work)
	}
	// vips treats "[...]" in a name as loader options; link such files under a safe name.
	if strings.Contains(abs, "[") {
		link := filepath.Join(work, "src"+filepath.Ext(abs))
		if err := os.Symlink(abs, link); err != nil {
			return "", err
		}
		return link, nil
	}
	return abs, nil
}

// rawFallback shows a RAW that has no big embedded preview (old DNGs, some NEFs): it is
// developed by vips where the build can decode raws (libraw), with the DNG's
// BaselineExposure applied, and otherwise its largest embedded preview of any size is
// used (small, but the right picture).
func rawFallback(ctx context.Context, t Tools, abs string, m Meta, work string) (string, error) {
	dev, derr := developRaw(ctx, t, abs, m, work)
	if derr == nil {
		return dev, nil
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	p, perr := rawPreview(ctx, t, abs, m, work, 1)
	if perr != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("raw: developing: %v; embedded preview: %w", derr, perr)
	}
	return p, nil
}

// maxBaselineEV bounds the exposure correction taken from a file.
const maxBaselineEV = 6

// developRaw decodes the raw with vips, fitted into the preview box, and applies the
// DNG's BaselineExposure: the gain 2^EV is applied in linear light with a highlight
// roll-off, f(x) = g·x / (1 + (g−1)·x), which is the plain gain for the shadows and
// mid-tones and maps white to white instead of clipping (libraw's own output is already
// brightened somewhat, so the plain gain blows ProRAW out). Without a correction the
// decoded image is used as it is. The result is close to, not the same as, the camera's
// rendering (Apple's gain tables and tone curve are not applied).
func developRaw(ctx context.Context, t Tools, abs string, m Meta, work string) (string, error) {
	if t.Vips == "" {
		return "", errors.New("vips not found")
	}
	// Without libraw vips still opens a DNG, as a TIFF: the first IFD, which is a tiny
	// thumbnail or the undemosaiced sensor data, so the result looks blown out or grey.
	if !vipsDecodesRaw(ctx, t.Vips) {
		return "", errors.New("vips has no raw loader")
	}
	in := abs
	if strings.Contains(abs, "[") {
		in = filepath.Join(work, "raw"+filepath.Ext(abs))
		if err := os.Symlink(abs, in); err != nil {
			return "", err
		}
	}
	s := strconv.Itoa(PreviewSize)
	base := filepath.Join(work, "dev.v")
	if err := runCmd(ctx, t.Vips, "thumbnail", in, base, s, "--height", s, "--size", "down"); err != nil {
		return "", err
	}
	ev := math.Max(-maxBaselineEV, math.Min(maxBaselineEV, m.BaselineExposure))
	if math.Abs(ev) < 0.01 {
		return base, nil
	}
	g := math.Pow(2, ev)
	f := func(v float64) string { return strconv.FormatFloat(v, 'f', 6, 64) }
	p := func(name string) string { return filepath.Join(work, name+".v") }
	steps := [][]string{
		{"colourspace", base, p("lin"), "scrgb"},
		{"linear", p("lin"), p("num"), f(g), "0"},
		{"linear", p("lin"), p("den"), f(g - 1), "1"},
		{"divide", p("num"), p("den"), p("mapped")},
		{"colourspace", p("mapped"), p("srgb"), "srgb"},
		{"cast", p("srgb"), p("out"), "uchar"},
	}
	for _, st := range steps {
		if err := runCmd(ctx, t.Vips, st...); err != nil {
			return "", err
		}
	}
	return p("out"), nil
}

var rawLoaders sync.Map // vips path -> bool

// vipsDecodesRaw reports whether the vips build has the libraw loader (dcrawload).
// The answer is remembered per binary; a failed probe counts as no.
func vipsDecodesRaw(ctx context.Context, vips string) bool {
	if v, ok := rawLoaders.Load(vips); ok {
		return v.(bool)
	}
	out, err := outCmd(ctx, vips, "-l", "foreign")
	if err != nil && ctx.Err() != nil {
		return false // not remembered: the probe was cut short
	}
	ok := err == nil && bytes.Contains(out, []byte("dcrawload"))
	rawLoaders.Store(vips, ok)
	return ok
}

// rawPreview extracts the embedded JPEG preview of a RAW: the first one of at least
// minBytes, or, for minBytes <= 1, the largest one there is.
func rawPreview(ctx context.Context, t Tools, abs string, m Meta, work string, minBytes int) (string, error) {
	if t.Exiftool == "" {
		return "", errors.New("exiftool not found")
	}
	out := filepath.Join(work, "preview.jpg")
	tags := []string{"-JpgFromRaw", "-PreviewImage", "-OtherImage"}
	largest := minBytes <= 1
	if largest {
		tags = append(tags, "-ThumbnailImage")
	}
	var best []byte
	for _, tag := range tags {
		data, err := outCmd(ctx, t.Exiftool, "-b", tag, "--", abs)
		if err != nil && ctx.Err() != nil {
			return "", ctx.Err()
		}
		// A JPEG starts with FFD8; exiftool prints nothing for a missing tag.
		if len(data) < max(minBytes, 2) || data[0] != 0xff || data[1] != 0xd8 {
			continue
		}
		// Apple ProRAW keeps its semantic masks (sky, skin...) as grey OtherImage JPEGs.
		if cfg, err := jpeg.DecodeConfig(bytes.NewReader(data)); err != nil || cfg.ColorModel == color.GrayModel {
			continue
		}
		if len(data) > len(best) {
			best = data
		}
		if !largest {
			break
		}
	}
	if best == nil {
		return "", errors.New("no embedded preview")
	}
	if err := os.WriteFile(out, best, 0o600); err != nil {
		return "", err
	}
	// The extracted JPEG may have lost the Orientation of the RAW; vips
	// rotates by that tag, so restore it when it is missing.
	if m.Orientation > 1 {
		got, _ := outCmd(ctx, t.Exiftool, "-n", "-s3", "-Orientation", out)
		if len(bytes.TrimSpace(got)) == 0 {
			if err := runCmd(ctx, t.Exiftool, "-overwrite_original", "-n", "-Orientation="+strconv.Itoa(m.Orientation), out); err != nil {
				return "", err
			}
		}
	}
	return out, nil
}

func videoFrame(ctx context.Context, t Tools, abs string, m Meta, work string) (string, error) {
	if t.Ffmpeg == "" {
		return "", errors.New("ffmpeg not found")
	}
	at := 1.0
	if d := float64(m.DurationMS) / 2000; m.DurationMS > 0 && d < at {
		at = d
	}
	out := filepath.Join(work, "frame.jpg")
	// ffmpeg applies the stream rotation itself.
	th := ffmpegThreads()
	err := runCmd(ctx, t.Ffmpeg, "-v", "error", "-y", "-threads", th, "-ss", strconv.FormatFloat(at, 'f', 3, 64),
		"-i", abs, "-frames:v", "1", "-q:v", "2", "-threads", th, out)
	if err != nil {
		return "", err
	}
	return out, nil
}

// TranscodeH264 writes a web-playable copy of src to dst: H.264 (CRF 23) up to
// 1920x1080 (1080x1920 for portrait, never upscaled), AAC audio, moov atom first.
func TranscodeH264(ctx context.Context, t Tools, src, dst string) error {
	if t.Ffmpeg == "" {
		return errors.New("ffmpeg not found")
	}
	abs, err := filepath.Abs(src)
	if err != nil {
		return err
	}
	return atomicWrite(dst, func(tmp string) error {
		th := ffmpegThreads()
		return runCmd(ctx, t.Ffmpeg, "-v", "error", "-y", "-threads", th, "-i", abs,
			"-map", "0:v:0", "-map", "0:a:0?",
			"-vf", `scale=w='min(iw,if(gte(iw,ih),1920,1080))':h='min(ih,if(gte(iw,ih),1080,1920))':force_original_aspect_ratio=decrease:force_divisible_by=2,format=yuv420p`,
			"-c:v", "libx264", "-preset", "veryfast", "-crf", "23", "-threads", th,
			"-c:a", "aac", "-b:a", "128k",
			"-movflags", "+faststart", "-f", "mp4", tmp)
	})
}

// atomicWrite lets fill write a temp file next to dst (keeping dst's extension,
// which vips and ffmpeg use to choose the format) and renames it into place.
func atomicWrite(dst string, fill func(tmp string) error) error {
	dir := filepath.Dir(dst)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".tmp-*"+filepath.Ext(dst))
	if err != nil {
		return err
	}
	tmp := f.Name()
	_ = f.Close()
	defer os.Remove(tmp)
	if err := fill(tmp); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

func runCmd(ctx context.Context, bin string, args ...string) error {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = toolEnv()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%s: %w: %s", filepath.Base(bin), err, strings.TrimSpace(tail(stderr.String())))
	}
	return nil
}

func outCmd(ctx context.Context, bin string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	err := cmd.Run()
	return stdout.Bytes(), err
}

func tail(s string) string {
	if len(s) > 500 {
		return s[len(s)-500:]
	}
	return s
}
