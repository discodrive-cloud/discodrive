package media

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestDetect_EnvOverride(t *testing.T) {
	for _, k := range []string{"PHOTOS_EXIFTOOL", "PHOTOS_VIPS", "PHOTOS_FFMPEG", "PHOTOS_FFPROBE"} {
		t.Setenv(k, "/bin/sh")
	}
	t.Setenv("PHOTOS_VIPS", "/nonexistent")
	if Detect().Available() {
		t.Fatal("Available() = true with a missing vips override")
	}

	t.Setenv("PHOTOS_VIPS", "/bin/sh")
	got := Detect()
	if !got.Available() {
		t.Fatalf("Available() = false, tools = %+v", got)
	}
	if got.Vips != "/bin/sh" {
		t.Fatalf("Vips = %q", got.Vips)
	}
}

// Each render is limited to a few threads: vips gets VIPS_CONCURRENCY=1 unless the server
// sets its own, ffmpeg -threads 2 unless PHOTOS_FFMPEG_THREADS says otherwise.
func TestRenderThreadLimits(t *testing.T) {
	t.Setenv("VIPS_CONCURRENCY", "")
	if got := envValue(toolEnv(), "VIPS_CONCURRENCY"); got != "1" {
		t.Fatalf("VIPS_CONCURRENCY = %q, want 1", got)
	}
	t.Setenv("VIPS_CONCURRENCY", "3")
	if got := envValue(toolEnv(), "VIPS_CONCURRENCY"); got != "3" {
		t.Fatalf("VIPS_CONCURRENCY = %q, want the server's 3", got)
	}
	t.Setenv("PHOTOS_FFMPEG_THREADS", "")
	if got := ffmpegThreads(); got != "2" {
		t.Fatalf("ffmpeg threads %q", got)
	}
	t.Setenv("PHOTOS_FFMPEG_THREADS", "6")
	if got := ffmpegThreads(); got != "6" {
		t.Fatalf("ffmpeg threads %q", got)
	}
	t.Setenv("PHOTOS_FFMPEG_THREADS", "junk")
	if got := ffmpegThreads(); got != "2" {
		t.Fatalf("ffmpeg threads %q", got)
	}

	// The limit reaches the process.
	fake := t.TempDir() + "/vips"
	out := t.TempDir() + "/env"
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho \"$VIPS_CONCURRENCY\" > "+out+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VIPS_CONCURRENCY", "")
	if err := runCmd(context.Background(), fake); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(out); strings.TrimSpace(string(b)) != "1" {
		t.Fatalf("vips saw VIPS_CONCURRENCY=%q", b)
	}
}

// envValue is the last value of key in env (later entries win, as for exec).
func envValue(env []string, key string) string {
	v := ""
	for _, kv := range env {
		if strings.HasPrefix(kv, key+"=") {
			v = kv[len(key)+1:]
		}
	}
	return v
}
