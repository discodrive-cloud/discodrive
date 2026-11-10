package ebook

import (
	"os"
	"path/filepath"
	"testing"
)

// Sample headers of each supported type (enough for the type to be recognised).
var (
	jpegBytes = append([]byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 16, 'J', 'F', 'I', 'F', 0}, make([]byte, 32)...)
	pngBytes  = append([]byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}, make([]byte, 32)...)
	gifBytes  = append([]byte("GIF89a"), make([]byte, 32)...)
	webpBytes = append([]byte("RIFF\x10\x00\x00\x00WEBPVP8 "), make([]byte, 32)...)
)

func TestWriteCoverExtMapping(t *testing.T) {
	cases := []struct {
		data    []byte
		wantExt string
	}{
		{jpegBytes, "jpg"},
		{pngBytes, "png"},
		{gifBytes, "gif"},
		{webpBytes, "webp"},
	}

	root := t.TempDir()
	for _, c := range cases {
		rel, err := WriteCover(root, "book-id-1", c.data)
		if err != nil {
			t.Fatalf("WriteCover(%s): %v", c.wantExt, err)
		}

		want := filepath.Join(".covers", "ebooks", "book-id-1."+c.wantExt)
		if rel != want {
			t.Errorf("relPath = %q, want %q", rel, want)
		}

		// File must exist on disk.
		abs := filepath.Join(root, rel)
		if _, serr := os.Stat(abs); serr != nil {
			t.Errorf("file not found at %q: %v", abs, serr)
		}
	}
}

func TestWriteCoverRelativePath(t *testing.T) {
	root := t.TempDir()
	rel, err := WriteCover(root, "mybook", pngBytes)
	if err != nil {
		t.Fatalf("WriteCover: %v", err)
	}
	if rel != filepath.Join(".covers", "ebooks", "mybook.png") {
		t.Errorf("relPath = %q, want .covers/ebooks/mybook.png", rel)
	}
	// Absolute path must exist.
	if _, serr := os.Stat(filepath.Join(root, rel)); serr != nil {
		t.Fatalf("cover file missing: %v", serr)
	}
}

func TestWriteCoverOverwrites(t *testing.T) {
	root := t.TempDir()
	first := jpegBytes
	second := append(append([]byte{}, jpegBytes...), "second content longer"...)

	rel1, err := WriteCover(root, "bk", first)
	if err != nil {
		t.Fatalf("first write: %v", err)
	}
	rel2, err := WriteCover(root, "bk", second)
	if err != nil {
		t.Fatalf("second write: %v", err)
	}
	if rel1 != rel2 {
		t.Errorf("paths differ on overwrite: %q vs %q", rel1, rel2)
	}

	got, err := os.ReadFile(filepath.Join(root, rel2))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != string(second) {
		t.Errorf("file content = %q, want %q", got, second)
	}
}

func TestRemoveCover(t *testing.T) {
	root := t.TempDir()
	rel, err := WriteCover(root, "rm-test", pngBytes)
	if err != nil {
		t.Fatalf("WriteCover: %v", err)
	}

	// Remove must succeed.
	if err := RemoveCover(root, rel); err != nil {
		t.Fatalf("RemoveCover: %v", err)
	}
	if _, serr := os.Stat(filepath.Join(root, rel)); !os.IsNotExist(serr) {
		t.Errorf("file still exists after RemoveCover")
	}

	// Second remove must be a no-op (no error).
	if err := RemoveCover(root, rel); err != nil {
		t.Errorf("RemoveCover on missing file: %v", err)
	}
}

func TestRemoveCoverEmpty(t *testing.T) {
	// Empty relPath must not error.
	if err := RemoveCover(t.TempDir(), ""); err != nil {
		t.Errorf("RemoveCover(\"\") = %v, want nil", err)
	}
}
