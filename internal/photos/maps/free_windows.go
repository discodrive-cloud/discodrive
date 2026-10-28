//go:build windows

package maps

import "math"

// freeBytes: no check on Windows (the server is not deployed there).
func freeBytes(string) (uint64, error) { return math.MaxUint64, nil }
