package storage_test

import (
	"os"
	"path/filepath"
	"testing"

	"discodrive/internal/storage"
)

func TestReadDirListsOneLevel(t *testing.T) {
	root := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(root, "u", "sub", "deep"), 0o755))
	must(os.WriteFile(filepath.Join(root, "u", "a.txt"), []byte("12345"), 0o644))
	must(os.Symlink("a.txt", filepath.Join(root, "u", "link")))

	items, err := storage.NewLocalDisk(root).ReadDir("u")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]storage.DirItem{}
	for _, it := range items {
		got[it.Name] = it
	}
	if len(got) != 3 {
		t.Fatalf("items %v: want a.txt, sub, link (one level, nothing from sub/deep)", items)
	}
	if it := got["a.txt"]; it.Kind != storage.KindFile || it.Size != 5 {
		t.Fatalf("a.txt = %+v", it)
	}
	if got["sub"].Kind != storage.KindDir {
		t.Fatalf("sub = %+v", got["sub"])
	}
	if got["link"].Kind != storage.KindOther {
		t.Fatalf("a symlink must be KindOther, got %+v", got["link"])
	}
	if _, err := storage.NewLocalDisk(root).ReadDir("u/missing"); !os.IsNotExist(err) {
		t.Fatalf("missing dir: err = %v, want not-exist", err)
	}
}
