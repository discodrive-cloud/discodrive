//go:build unix

package storage

import (
	"fmt"
	"os"
	"runtime"

	"golang.org/x/sys/unix"
)

func openRegular(r *os.Root, name string, flags int) (*os.File, error) {
	// O_NONBLOCK prevents a substituted FIFO from hanging a request. Check regular
	// file type before any read/write, and truncate only after that check.
	truncate := flags&os.O_TRUNC != 0
	// os.Root resolves relative symlinks in userspace, even with O_NOFOLLOW.
	// Use openat directly for the final component to enforce the kernel check.
	parent, err := r.Open(".")
	if err != nil {
		return nil, err
	}
	fd, err := unix.Openat(int(parent.Fd()), name, (flags&^os.O_TRUNC)|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0644)
	parent.Close()
	if err != nil {
		return nil, &os.PathError{Op: "openat", Path: name, Err: err}
	}
	f := os.NewFile(uintptr(fd), name)
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		f.Close()
		if err != nil {
			return nil, err
		}
		return nil, ErrPathEscape
	}
	if truncate {
		if err := f.Truncate(0); err != nil {
			f.Close()
			return nil, err
		}
	}
	return f, nil
}

func renameAt(src *os.Root, from string, dst *os.Root, to string) error {
	a, err := src.Open(".")
	if err != nil {
		return err
	}
	defer a.Close()
	b, err := dst.Open(".")
	if err != nil {
		return err
	}
	defer b.Close()
	err = unix.Renameat(int(a.Fd()), from, int(b.Fd()), to)
	// Keep descriptors live across the syscall.
	runtime.KeepAlive(a)
	runtime.KeepAlive(b)
	if err != nil {
		return &os.LinkError{Op: "renameat", Old: from, New: to, Err: err}
	}
	return nil
}

func descriptorPath(f *os.File) (string, error) {
	switch runtime.GOOS {
	case "linux":
		return fmt.Sprintf("/proc/self/fd/%d", f.Fd()), nil
	case "darwin":
		return fmt.Sprintf("/dev/fd/%d", f.Fd()), nil
	default:
		return "", errUnsupportedFilesystem
	}
}
