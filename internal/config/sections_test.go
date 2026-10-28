package config_test

import (
	"testing"

	"discodrive/internal/config"
)

// Sections are on unless switched off explicitly; each switch is its own.
func TestLoadSections(t *testing.T) {
	for _, k := range []string{"MUSIC_ENABLED", "BOOKS_ENABLED", "PHOTOS_ENABLED"} {
		t.Setenv(k, "")
	}
	if c := config.Load(); !c.MusicEnabled || !c.BooksEnabled || !c.PhotosEnabled {
		t.Fatalf("defaults: %v %v %v", c.MusicEnabled, c.BooksEnabled, c.PhotosEnabled)
	}
	t.Setenv("MUSIC_ENABLED", "false")
	t.Setenv("PHOTOS_ENABLED", "true")
	if c := config.Load(); c.MusicEnabled || !c.BooksEnabled || !c.PhotosEnabled {
		t.Fatalf("music off: %v %v %v", c.MusicEnabled, c.BooksEnabled, c.PhotosEnabled)
	}
	t.Setenv("MUSIC_ENABLED", "")
	t.Setenv("BOOKS_ENABLED", "0")
	t.Setenv("PHOTOS_ENABLED", "FALSE")
	if c := config.Load(); !c.MusicEnabled || c.BooksEnabled || c.PhotosEnabled {
		t.Fatalf("books and photos off: %v %v %v", c.MusicEnabled, c.BooksEnabled, c.PhotosEnabled)
	}
}
