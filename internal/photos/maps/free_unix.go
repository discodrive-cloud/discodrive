//go:build !windows

package maps

import "syscall"

// freeBytes is the space available to unprivileged writes on the volume holding dir
// (or its nearest existing parent).
func freeBytes(dir string) (uint64, error) {
	for {
		var st syscall.Statfs_t
		err := syscall.Statfs(dir, &st)
		if err == nil {
			return uint64(st.Bavail) * uint64(st.Bsize), nil
		}
		parent := dirOf(dir)
		if parent == dir {
			return 0, err
		}
		dir = parent
	}
}
