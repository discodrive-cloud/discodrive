package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type chownCall struct {
	path     string
	uid, gid int
}

func recordChown(calls *[]chownCall) func(string, int, int) error {
	return func(path string, uid, gid int) error {
		*calls = append(*calls, chownCall{path, uid, gid})
		return nil
	}
}

// foreign is a uid/gid the test process never runs as.
const foreign = 64999

func answer(ok bool) func(string, int, int) bool {
	return func(string, int, int) bool { return ok }
}

func TestPrepareStorageLeavesWritableDirAlone(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "photo.jpg"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	var calls []chownCall
	var out bytes.Buffer
	if code := prepareStorage(dir, foreign, foreign, answer(true), recordChown(&calls), &out); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if len(calls) != 0 {
		t.Fatalf("chowned a directory the server can already write to: %v", calls)
	}
}

func TestPrepareStorageHandsFreshCloneToServer(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".gitkeep"), nil, 0o664); err != nil {
		t.Fatal(err)
	}
	var calls []chownCall
	var out bytes.Buffer
	if code := prepareStorage(dir, foreign, foreign, answer(false), recordChown(&calls), &out); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if len(calls) != 1 || calls[0] != (chownCall{dir, foreign, foreign}) {
		t.Fatalf("chown calls %v", calls)
	}
}

func TestPrepareStorageCreatesMissingDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	var calls []chownCall
	var out bytes.Buffer
	if code := prepareStorage(dir, foreign, foreign, answer(false), recordChown(&calls), &out); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Fatalf("directory not created: %v", err)
	}
	if len(calls) != 1 || calls[0].path != dir {
		t.Fatalf("chown calls %v", calls)
	}
}

func TestPrepareStorageRefusesToTouchExistingData(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "photo.jpg"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	var calls []chownCall
	var out bytes.Buffer
	if code := prepareStorage(dir, foreign, foreign, answer(false), recordChown(&calls), &out); code == 0 {
		t.Fatal("succeeded on a directory the server can't write to")
	}
	if len(calls) != 0 {
		t.Fatalf("chowned someone's existing data: %v", calls)
	}
	if !strings.Contains(out.String(), "chown") {
		t.Fatalf("no hint on how to fix it: %s", out.String())
	}
}

func TestWriteProbe(t *testing.T) {
	dir := t.TempDir()
	if code := runWriteProbe(dir); code != 0 {
		t.Fatalf("probe failed on a writable directory: exit %d", code)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("probe left files behind: %v", entries)
	}
	if os.Getuid() == 0 {
		t.Skip("root writes anywhere")
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	if code := runWriteProbe(dir); code == 0 {
		t.Fatal("probe succeeded on a read-only directory")
	}
}
