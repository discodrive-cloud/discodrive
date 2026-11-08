package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"discodrive/internal/db"
	"discodrive/internal/photos"
	"discodrive/internal/photos/media"
)

// A preview that cannot be rendered must not be rendered again on every request: the
// public album routes reach it without an account, and each attempt holds a shared
// render slot and the exiftool lock.
func TestPreviewFailureIsRemembered(t *testing.T) {
	e := newPhotosEnv(t, false, 0)
	count := e.fakeMediaTools()
	a := e.asset(e.user, assetOpts{})
	renders := func() int {
		b, _ := os.ReadFile(count)
		return strings.Count(string(b), "x")
	}
	for i := 0; i < 3; i++ {
		e.do(e.tok, "GET", "/me/photos/v1/assets/"+db.UUIDString(a.ID)+"/preview", nil)
	}
	if n := renders(); n != 1 {
		t.Fatalf("3 requests for a failing preview ran the renderer %d times, want 1", n)
	}
}

// fakeMediaTools wires the gallery with stand-in tools: a vips that always fails and
// counts its runs (the returned file), and an exiftool that answers every request with
// nothing. The gallery is switched on for the first user.
func (e *photosEnv) fakeMediaTools() (renderCount string) {
	t := e.t
	dir := t.TempDir()
	count := filepath.Join(dir, "renders")
	vips := filepath.Join(dir, "vips")
	exiftool := filepath.Join(dir, "exiftool")
	if err := os.WriteFile(vips, []byte("#!/bin/sh\necho x >> "+count+"\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// speaks just enough of the -stay_open protocol: an empty answer per request
	if err := os.WriteFile(exiftool, []byte("#!/bin/sh\nwhile read l; do [ \"$l\" = \"-execute\" ] && echo '[]' && echo '{ready}'; done\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	tools := media.Tools{Exiftool: exiftool, Vips: vips, Ffmpeg: "/bin/false", Ffprobe: "/bin/false"}
	exif, err := media.NewExif(tools)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = exif.Close() })
	e.idx = photos.NewIndexer(e.q, e.root, exif, tinyGeo{})
	e.th = photos.NewThumbs(e.q, e.root, tools, 0, exif)
	e.h = e.router()
	if _, err := e.q.UpsertPhotoSettings(e.ctx, db.UpsertPhotoSettingsParams{UserID: e.user.ID, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	return count
}
