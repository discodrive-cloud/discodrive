package storage

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// cleanRelative rejects traversal rather than silently redirecting it elsewhere.
func cleanRelative(rel string) (string, error) {
	rel = strings.ReplaceAll(rel, "\\", "/")
	if !filepath.IsLocal(rel) || strings.ContainsRune(rel, 0) {
		return "", ErrPathEscape
	}
	for _, part := range strings.Split(rel, "/") {
		if part == ".." {
			return "", ErrPathEscape
		}
	}
	return filepath.Clean(rel), nil
}

func rejectLink(r *os.Root, name string) error {
	info, err := r.Lstat(name)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return ErrPathEscape
	}
	return nil
}

// childDirectory pins a directory and verifies its identity after opening it.
// A symlink swapped in between Lstat and OpenRoot cannot substitute another tree.
func childDirectory(r *os.Root, name string, create bool) (*os.Root, error) {
	if create {
		if err := r.Mkdir(name, 0755); err != nil && !os.IsExist(err) {
			return nil, err
		}
	}
	before, err := r.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return nil, ErrPathEscape
	}
	child, err := r.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	after, err := child.Stat(".")
	if err != nil || !os.SameFile(before, after) {
		child.Close()
		if err != nil {
			return nil, err
		}
		return nil, ErrPathEscape
	}
	return child, nil
}

func (d *LocalDisk) directory(rel string, create bool) (*os.Root, error) {
	rel, err := cleanRelative(rel)
	if err != nil {
		return nil, err
	}
	// The configured root is trusted; symlinks below it are never trusted.
	if create {
		if err := os.MkdirAll(d.root, 0755); err != nil {
			return nil, err
		}
	}
	r, err := os.OpenRoot(d.root)
	if err != nil {
		return nil, err
	}
	if rel == "." {
		return r, nil
	}
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		next, err := childDirectory(r, part, create)
		r.Close()
		if err != nil {
			return nil, err
		}
		r = next
	}
	return r, nil
}

func (d *LocalDisk) parent(rel string, create bool) (*os.Root, string, error) {
	rel, err := cleanRelative(rel)
	if err != nil {
		return nil, "", err
	}
	if rel == "." {
		return nil, "", ErrPathEscape
	}
	r, err := d.directory(filepath.Dir(rel), create)
	return r, filepath.Base(rel), err
}

func (d *LocalDisk) openFile(rel string, flags int, createParents bool) (*os.File, error) {
	r, name, err := d.parent(rel, createParents)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return openRegular(r, name, flags)
}

// OpenAbsolute opens an absolute storage path through the same protected path
// traversal as Open. Useful at boundaries with path-based media parsers.
func (d *LocalDisk) OpenAbsolute(abs string) (*os.File, error) {
	root, err := filepath.Abs(d.root)
	if err != nil {
		return nil, err
	}
	abs, err = filepath.Abs(abs)
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return nil, err
	}
	return d.Open(rel)
}

// Pin provides a stable descriptor path for libraries that only accept filenames.
// Keep the file open until the library finishes. No untrusted pathname is reopened.
func (d *LocalDisk) Pin(abs string) (*os.File, string, error) {
	f, err := d.OpenAbsolute(abs)
	if err != nil {
		return nil, "", err
	}
	path, err := descriptorPath(f)
	if err != nil {
		f.Close()
		return nil, "", err
	}
	return f, path, nil
}

var errUnsupportedFilesystem = errors.New("secure local filesystem access unsupported on this platform")
