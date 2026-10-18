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

func TestPrepareStorageLeavesWritableDirAlone(t *testing.T) {
	dir := t.TempDir()
	var calls []chownCall
	var out bytes.Buffer
	if code := prepareStorage(dir, os.Getuid(), os.Getgid(), recordChown(&calls), &out); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if len(calls) != 0 {
		t.Fatalf("chowned a directory the server can already write to: %v", calls)
	}
}

func TestPrepareStorageLeavesWorldWritableDirAlone(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	var calls []chownCall
	var out bytes.Buffer
	if code := prepareStorage(dir, foreign, foreign, recordChown(&calls), &out); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if len(calls) != 0 {
		t.Fatalf("chowned a world-writable directory: %v", calls)
	}
}

func TestPrepareStorageHandsFreshCloneToServer(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".gitkeep"), nil, 0o664); err != nil {
		t.Fatal(err)
	}
	var calls []chownCall
	var out bytes.Buffer
	if code := prepareStorage(dir, foreign, foreign, recordChown(&calls), &out); code != 0 {
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
	if code := prepareStorage(dir, foreign, foreign, recordChown(&calls), &out); code != 0 {
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
	if code := prepareStorage(dir, foreign, foreign, recordChown(&calls), &out); code == 0 {
		t.Fatal("succeeded on a directory the server can't write to")
	}
	if len(calls) != 0 {
		t.Fatalf("chowned someone's existing data: %v", calls)
	}
	if !strings.Contains(out.String(), "chown") {
		t.Fatalf("no hint on how to fix it: %s", out.String())
	}
}
