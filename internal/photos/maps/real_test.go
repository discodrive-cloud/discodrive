package maps

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// TestMaps_RealExtract talks to the real protomaps build server; run by hand with
// PHOTOS_MAPS_REAL=<region id> (it downloads the extract into a temp dir).
func TestMaps_RealExtract(t *testing.T) {
	id := os.Getenv("PHOTOS_MAPS_REAL")
	if id == "" {
		t.Skip("set PHOTOS_MAPS_REAL=<region> to run")
	}
	reg, ok := FindRegion(id)
	if !ok {
		t.Fatal("unknown region")
	}
	if z := os.Getenv("PHOTOS_MAPS_MAXZOOM"); z != "" {
		n, err := strconv.Atoi(z)
		if err != nil {
			t.Fatal(err)
		}
		reg.MaxZoom = int8(n)
	}
	bin, err := exec.LookPath("pmtiles")
	if err != nil {
		t.Skip("pmtiles CLI not installed")
	}
	m := New(context.Background(), nil, t.TempDir(), Config{Pmtiles: bin})
	src, err := m.latestBuild(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "region.pmtiles")
	start := time.Now()
	if err := m.cfg.Extract(context.Background(), src, reg.BBox, reg.MaxZoom, out, func(int64) {}); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(out)
	t.Logf("build %s region %s maxzoom %d: %d bytes in %v", src, reg.ID, reg.MaxZoom, fi.Size(), time.Since(start))
	if d := os.Getenv("PHOTOS_MAPS_KEEP"); d != "" {
		_ = os.Rename(out, d)
	}
}
