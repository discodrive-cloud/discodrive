package config_test

import (
	"runtime"
	"testing"
	"time"

	"discodrive/internal/config"
)

// The gallery's defaults stay modest on a big machine: at most four render workers.
func TestLoadPhotosDefaults(t *testing.T) {
	t.Setenv("PHOTOS_WORKERS", "")
	t.Setenv("PHOTOS_EXIF_TIMEOUT", "")
	c := config.Load()
	if want := max(1, min(4, runtime.NumCPU()/2)); c.PhotosWorkers != want {
		t.Fatalf("PhotosWorkers = %d, want %d", c.PhotosWorkers, want)
	}
	if c.PhotosExifTimeout != time.Minute {
		t.Fatalf("PhotosExifTimeout = %s", c.PhotosExifTimeout)
	}
	t.Setenv("PHOTOS_WORKERS", "0")
	t.Setenv("PHOTOS_EXIF_TIMEOUT", "5")
	c = config.Load()
	if c.PhotosWorkers != 0 || c.PhotosExifTimeout != 5*time.Second {
		t.Fatalf("overrides: %d %s", c.PhotosWorkers, c.PhotosExifTimeout)
	}
}
