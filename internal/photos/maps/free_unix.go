//go:build !windows

package maps

import "discodrive/internal/storage"

// freeBytes is the space available to unprivileged writes on the volume holding dir
// (or its nearest existing parent). storage.DiskUsage counts in the filesystem's
// fragment size: block size times free blocks overstates it 256 times on virtiofs.
func freeBytes(dir string) (uint64, error) {
	for {
		_, free, err := storage.DiskUsage(dir)
		if err == nil {
			return free, nil
		}
		parent := dirOf(dir)
		if parent == dir {
			return 0, err
		}
		dir = parent
	}
}
