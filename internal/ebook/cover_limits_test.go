package ebook

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func tinyPNG(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// Covers live in .covers/, outside the user's quota. A 64 KB comic archive whose first
// "image" is 64 MiB of zeros used to put 64 MiB there; what is cached must be a real
// picture of a sane size.
func TestWriteCover_RefusesWhatIsNotAPictureOrIsHuge(t *testing.T) {
	root := t.TempDir()
	if _, err := WriteCover(root, "b1", make([]byte, 1<<20)); err == nil {
		t.Error("a megabyte of zeros was cached as a cover")
	}
	huge := append(tinyPNG(t), make([]byte, maxCoverBytes)...)
	if _, err := WriteCover(root, "b2", huge); err == nil {
		t.Errorf("a %d MiB cover was cached", len(huge)>>20)
	}
	entries, _ := os.ReadDir(filepath.Join(root, ".covers", "ebooks"))
	if len(entries) != 0 {
		t.Errorf("%d file(s) left in the cover cache", len(entries))
	}
}

// The extension follows the bytes, not the type the book claims, and a cover that changes
// type does not leave the previous file behind.
func TestWriteCover_ExtensionFromContentAndNoOrphan(t *testing.T) {
	root := t.TempDir()
	rel, err := WriteCover(root, "b1", tinyPNG(t))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Ext(rel) != ".png" {
		t.Errorf("path %q, want a .png", rel)
	}
	jpeg := append([]byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 16, 'J', 'F', 'I', 'F', 0}, make([]byte, 64)...)
	if _, err := WriteCover(root, "b1", jpeg); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(filepath.Join(root, ".covers", "ebooks"))
	if len(entries) != 1 || entries[0].Name() != "b1.jpg" {
		t.Errorf("cover cache holds %v, want only b1.jpg", entries)
	}
}
