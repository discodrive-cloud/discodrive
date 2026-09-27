package worker

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// Staged upload chunks and the bootstrap secret are written by the service itself;
// watching them made every chunk of an upload trigger a full rescan.
func TestWatchDirsSkipsServiceAreas(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"user/music", ".uploads", ".bootstrap", ".tmp", "user/.versions/x"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	var got []string
	for _, p := range watchDirs(root) {
		rel, _ := filepath.Rel(root, p)
		got = append(got, rel)
	}
	slices.Sort(got)
	want := []string{".", "user", "user/music"}
	if !slices.Equal(got, want) {
		t.Fatalf("watched %v, want %v", got, want)
	}
}
