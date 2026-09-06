//go:build !unix

package fsx

import "errors"

// DiskFree is unsupported on this platform.
func DiskFree(string) (int64, int, error) {
	return 0, 0, errors.New("disk space check not supported on this platform")
}
