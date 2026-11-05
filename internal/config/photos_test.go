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

func TestLoadFacesConfig(t *testing.T) {
	for _, k := range []string{"PHOTOS_ML_URL", "PHOTOS_ML_MODEL", "PHOTOS_FACES_WORKERS", "PHOTOS_FACES_MIN_SCORE",
		"PHOTOS_FACES_MAX_DISTANCE", "PHOTOS_FACES_MIN_FACES"} {
		t.Setenv(k, "")
	}
	c := config.Load()
	if c.PhotosMLURL != "" || c.PhotosMLModel != "auraface" || c.PhotosFacesWorkers != 2 ||
		c.PhotosFacesMinScore != 0.7 || c.PhotosFacesMaxDistance != 0.6 || c.PhotosFacesMinFaces != 3 {
		t.Fatalf("defaults: %+v", c)
	}
	t.Setenv("PHOTOS_ML_URL", "http://ml:3003")
	t.Setenv("PHOTOS_FACES_MAX_DISTANCE", "0.45")
	t.Setenv("PHOTOS_FACES_MIN_FACES", "bad")
	c = config.Load()
	if c.PhotosMLURL != "http://ml:3003" || c.PhotosFacesMaxDistance != 0.45 || c.PhotosFacesMinFaces != 3 {
		t.Fatalf("set: %+v", c)
	}
	// Immich's models keep their own threshold unless one is given.
	t.Setenv("PHOTOS_FACES_MAX_DISTANCE", "")
	t.Setenv("PHOTOS_ML_MODEL", "buffalo_l")
	if c = config.Load(); c.PhotosFacesMaxDistance != 0.5 {
		t.Fatalf("buffalo_l threshold: %v", c.PhotosFacesMaxDistance)
	}
}
