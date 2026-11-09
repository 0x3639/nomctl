//go:build !unix

package fsx

import "errors"

func ReclaimableBytes(string) (int64, error) {
	return 0, errors.New("allocated space check not supported on this platform")
}

// DiskCapacity is unsupported on this platform.
func DiskCapacity(string) (int64, int64, error) {
	return 0, 0, errors.New("disk space check not supported on this platform")
}

// DiskFree is unsupported on this platform.
func DiskFree(string) (int64, int, error) {
	return 0, 0, errors.New("disk space check not supported on this platform")
}
