// Package storage implements "files as files on disk" — a mirror of the user's
// tree on the local filesystem. Cures the Seafile trauma:
// service dies → you open the folder and the files are still there.
package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
)

// DiskEntry is a single entry in the on-disk tree (relative path from the root).
type DiskEntry struct {
	Rel   string
	IsDir bool
}

// ErrPathEscape is returned when a relative path attempts to escape the storage root.
var ErrPathEscape = errors.New("path escapes storage root")

// Storage abstracts the file store (designed to support future S3 backends).
type Storage interface {
	// WriteFile writes content at the given relative path (creating parents),
	// and returns the size and sha256 hex digest.
	WriteFile(rel string, r io.Reader) (size int64, sha256hex string, err error)
	// Mkdir creates the directory and any missing parents.
	Mkdir(rel string) error
	// Move renames/moves a path (file or directory).
	Move(oldRel, newRel string) error
	// Copy copies a file src→dst (creating dst parents). Used for version snapshots.
	Copy(srcRel, dstRel string) error
	// Append appends data to the end of a file (creates it if absent). Used for chunks.
	Append(rel string, r io.Reader) error
	// Size returns the current size of a file, or 0 if it does not exist yet.
	Size(rel string) (int64, error)
	// Truncate shrinks a file to size. Used to roll a failed chunk append back to the
	// length the staging file had before it started.
	Truncate(rel string, size int64) error
	// Remove deletes a path recursively (used by GC; not called on soft-delete).
	Remove(rel string) error
	// Exists checks for a regular file or directory without following symlinks.
	Exists(rel string) (bool, error)
	// Open opens a file for reading.
	Open(rel string) (*os.File, error)
	// Walk returns the subtree under rel (parents before children), with relative
	// paths. Unreadable directories are skipped rather than aborting the walk;
	// partial reports whether anything was skipped (the listing is incomplete).
	Walk(rel string) (entries []DiskEntry, partial bool, err error)
}

// LocalDisk is a Storage implementation backed by the local filesystem rooted at root.
type LocalDisk struct {
	root string
}

func NewLocalDisk(root string) *LocalDisk {
	return &LocalDisk{root: filepath.Clean(root)}
}

func (d *LocalDisk) WriteFile(rel string, r io.Reader) (int64, string, error) {
	f, err := d.openFile(rel, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, true)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), r)
	if err != nil {
		return 0, "", err
	}
	if err := f.Sync(); err != nil {
		return 0, "", err
	}
	return n, hex.EncodeToString(h.Sum(nil)), nil
}

func (d *LocalDisk) Mkdir(rel string) error {
	r, err := d.directory(rel, true)
	if err != nil {
		return err
	}
	return r.Close()
}

func (d *LocalDisk) Move(oldRel, newRel string) error {
	// Hold both parents open. Rename via their descriptors, without resolving
	// either original path again (including during concurrent directory swaps).
	old, oldName, err := d.parent(oldRel, false)
	if err != nil {
		return err
	}
	defer old.Close()
	dest, newName, err := d.parent(newRel, true)
	if err != nil {
		return err
	}
	defer dest.Close()
	if err := rejectLink(old, oldName); err != nil {
		return err
	}
	if err := rejectLink(dest, newName); err != nil && !os.IsNotExist(err) {
		return err
	}
	return renameAt(old, oldName, dest, newName)
}

func (d *LocalDisk) Copy(srcRel, dstRel string) error {
	in, err := d.Open(srcRel)
	if err != nil {
		return err
	}
	defer in.Close()
	_, _, err = d.WriteFile(dstRel, in)
	return err
}

func (d *LocalDisk) Append(rel string, r io.Reader) error {
	f, err := d.openFile(rel, os.O_APPEND|os.O_CREATE|os.O_WRONLY, true)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := io.Copy(f, r); err != nil {
		return err
	}
	return f.Sync()
}

func (d *LocalDisk) Size(rel string) (int64, error) {
	f, err := d.Open(rel)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return 0, err
	}
	return fi.Size(), nil
}

func (d *LocalDisk) Truncate(rel string, size int64) error {
	f, err := d.openFile(rel, os.O_WRONLY, false)
	if size == 0 && os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Truncate(size)
}

func (d *LocalDisk) Remove(rel string) error {
	r, name, err := d.parent(rel, false)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer r.Close()
	// RemoveAll unlinks symlinks themselves; it never traverses them.
	return r.RemoveAll(name)
}

func (d *LocalDisk) Exists(rel string) (bool, error) {
	r, name, err := d.parent(rel, false)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer r.Close()
	info, err := r.Lstat(name)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.IsDir() && !info.Mode().IsRegular() {
		return false, ErrPathEscape
	}
	return true, nil
}

func (d *LocalDisk) Open(rel string) (*os.File, error) {
	return d.openFile(rel, os.O_RDONLY, false)
}

func (d *LocalDisk) Walk(rel string) ([]DiskEntry, bool, error) {
	r, err := d.directory(rel, false)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, true, err
	}
	defer r.Close()
	var out []DiskEntry
	partial := false
	var walk func(*os.Root, string)
	walk = func(root *os.Root, prefix string) {
		f, err := root.Open(".")
		if err != nil {
			partial = true
			return
		}
		entries, err := f.ReadDir(-1)
		f.Close()
		if err != nil {
			partial = true
			return
		}
		// Match filepath.WalkDir's stable ordering and parent-before-child contract.
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, e := range entries {
			name := e.Name()
			info, err := root.Lstat(name)
			if err != nil {
				partial = true
				continue
			}
			if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
				partial = true
				continue
			}
			entryPath := filepath.Join(prefix, name)
			if info.IsDir() {
				child, err := childDirectory(root, name, false)
				if err != nil {
					partial = true
					continue
				}
				out = append(out, DiskEntry{Rel: filepath.ToSlash(entryPath), IsDir: true})
				walk(child, entryPath)
				child.Close()
			} else {
				out = append(out, DiskEntry{Rel: filepath.ToSlash(entryPath)})
			}
		}
	}
	walk(r, rel)
	return out, partial, nil
}
