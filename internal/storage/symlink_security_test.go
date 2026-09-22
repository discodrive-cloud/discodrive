package storage

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalDiskRejectsSymlinks(t *testing.T) {
	for _, target := range []string{"outside", "other-user"} {
		t.Run(target, func(t *testing.T) {
			root := t.TempDir()
			secretDir := t.TempDir()
			if target == "other-user" {
				secretDir = filepath.Join(root, "bob")
			}
			if err := os.MkdirAll(secretDir, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(root, "alice"), 0755); err != nil {
				t.Fatal(err)
			}
			secret := filepath.Join(secretDir, "secret")
			if err := os.WriteFile(secret, []byte("private"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(secretDir, filepath.Join(root, "alice", "link")); err != nil {
				t.Fatal(err)
			}
			d := NewLocalDisk(root)
			if f, err := d.Open("alice/link/secret"); err == nil {
				data, _ := io.ReadAll(f)
				f.Close()
				t.Errorf("read leaked: %q", data)
			}
			if _, _, err := d.WriteFile("alice/link/secret", strings.NewReader("overwrite")); err == nil {
				t.Error("write followed symlink")
			}
			data, err := os.ReadFile(secret)
			if err != nil || string(data) != "private" {
				t.Errorf("secret modified: %q, %v", data, err)
			}
		})
	}
}

func TestLocalDiskSymlinkOperations(t *testing.T) {
	for _, parent := range []bool{false, true} {
		for name, op := range map[string]func(*LocalDisk, string) error{
			"open": func(d *LocalDisk, p string) error {
				f, e := d.Open(p)
				if f != nil {
					f.Close()
				}
				return e
			},
			"write":     func(d *LocalDisk, p string) error { _, _, e := d.WriteFile(p, strings.NewReader("bad")); return e },
			"append":    func(d *LocalDisk, p string) error { return d.Append(p, strings.NewReader("bad")) },
			"truncate":  func(d *LocalDisk, p string) error { return d.Truncate(p, 0) },
			"size":      func(d *LocalDisk, p string) error { _, e := d.Size(p); return e },
			"copy-from": func(d *LocalDisk, p string) error { return d.Copy(p, "copy") },
			"copy-to":   func(d *LocalDisk, p string) error { return d.Copy("source", p) },
			"move-from": func(d *LocalDisk, p string) error { return d.Move(p, "moved") },
			"move-to":   func(d *LocalDisk, p string) error { return d.Move("source", p) },
		} {
			t.Run(fmt.Sprintf("%s/parent=%t", name, parent), func(t *testing.T) {
				root := t.TempDir()
				outside := t.TempDir()
				if err := os.WriteFile(filepath.Join(root, "source"), []byte("source"), 0600); err != nil {
					t.Fatal(err)
				}
				secret := filepath.Join(outside, "secret")
				if err := os.WriteFile(secret, []byte("private"), 0600); err != nil {
					t.Fatal(err)
				}
				path := "link"
				target := secret
				if parent {
					target = outside
					path = "link/secret"
				}
				if err := os.Symlink(target, filepath.Join(root, "link")); err != nil {
					t.Fatal(err)
				}
				if err := op(NewLocalDisk(root), path); err == nil {
					t.Fatal("operation followed symlink")
				}
				got, err := os.ReadFile(secret)
				if err != nil || string(got) != "private" {
					t.Fatalf("secret altered: %q %v", got, err)
				}
			})
		}
	}
}

func TestLocalDiskWalkAndRemoveDoNotFollowLinks(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "alice"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "alice", "ordinary"), []byte("ok"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "alice", "dirlink")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(root, "alice", "filelink")); err != nil {
		t.Fatal(err)
	}
	d := NewLocalDisk(root)
	entries, partial, err := d.Walk("alice")
	if err != nil || !partial || len(entries) != 1 || entries[0].Rel != "alice/ordinary" {
		t.Fatalf("walk: %+v partial=%t %v", entries, partial, err)
	}
	if err := d.Mkdir("alice/dirlink/subdir"); err == nil {
		t.Fatal("mkdir followed symlink")
	}
	if err := d.Remove("alice/dirlink/secret"); err == nil {
		t.Fatal("remove followed symlink parent")
	}
	if err := d.Remove("alice"); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(outside, "secret")); err != nil || string(got) != "private" {
		t.Fatalf("remove escaped: %q %v", got, err)
	}
}

func TestPinnedFileSurvivesPathReplacement(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	path := filepath.Join(root, "file")
	if err := os.WriteFile(path, []byte("own"), 0600); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(outside, "secret")
	if err := os.WriteFile(secret, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	f, pinned, err := NewLocalDisk(root).Pin(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(pinned)
	if err != nil || string(data) != "own" {
		t.Fatalf("pinned read: %q %v", data, err)
	}
}

func TestLocalDiskConcurrentDirectorySwap(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "dir")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secret"), []byte("own"), 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		for {
			select {
			case <-done:
				return
			default:
			}
			os.Rename(dir, dir+"-held")
			os.Symlink(outside, dir)
			os.Remove(dir)
			os.Rename(dir+"-held", dir)
		}
	}()
	defer func() { close(done); <-stopped }()
	d := NewLocalDisk(root)
	for i := 0; i < 400; i++ {
		if f, err := d.Open("dir/secret"); err == nil {
			data, _ := io.ReadAll(f)
			f.Close()
			if string(data) == "private" {
				t.Fatal("race leaked secret")
			}
		}
		_, _, _ = d.WriteFile("dir/secret", strings.NewReader("own"))
	}
	data, err := os.ReadFile(filepath.Join(outside, "secret"))
	if err != nil || string(data) != "private" {
		t.Fatalf("race overwrote secret: %q %v", data, err)
	}
}

func TestLocalDiskRejectsRelativeLeafSymlink(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "target"), []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	d := NewLocalDisk(root)
	if f, err := d.Open("link"); err == nil {
		f.Close()
		t.Fatal("followed relative symlink inside storage")
	}
	if _, _, err := d.WriteFile("link", strings.NewReader("bad")); err == nil {
		t.Fatal("overwrote relative symlink target")
	}
	if got, err := os.ReadFile(filepath.Join(root, "target")); err != nil || string(got) != "private" {
		t.Fatalf("target changed: %q %v", got, err)
	}
}
