// Package musictest synthesizes audio files for tests of packages that index music.
// It is imported only from tests, so the testing package never reaches the server.
package musictest

import (
	"os/exec"
	"testing"
)

// RequireFFmpeg skips the test when ffmpeg is not on PATH.
func RequireFFmpeg(t testing.TB) {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not found on PATH")
	}
}

// SynthesizeAudio writes a one-second tone with the given tags to dst.
func SynthesizeAudio(t testing.TB, dst, title, artist, album, format string) {
	t.Helper()
	args := []string{
		"-y",
		"-f", "lavfi",
		"-i", "sine=frequency=440:duration=1",
		"-metadata", "title=" + title,
		"-metadata", "artist=" + artist,
		"-metadata", "album=" + album,
	}
	if format == "mp3" {
		args = append(args, "-codec:a", "libmp3lame", "-b:a", "64k")
	}
	args = append(args, dst)
	out, err := exec.Command("ffmpeg", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("ffmpeg: %v\n%s", err, out)
	}
}
