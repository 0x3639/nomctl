//go:build unix

package fsx

import (
	"os"
	"syscall"
)

// ReclaimableBytes estimates allocated space released by unlinking a file.
// Hard-linked files are conservatively excluded because another link may keep
// their blocks allocated. Shared/COW blocks and open files can reduce recovery.
func ReclaimableBytes(path string) (int64, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return 0, err
	}
	stat, ok := st.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink > 1 {
		return 0, nil
	}
	return stat.Blocks * 512, nil
}

// DiskCapacity returns bytes available to ordinary users and bytes already
// used. Reserved blocks are excluded, matching df's percentage denominator.
func DiskCapacity(path string) (available, used int64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	bsize := int64(st.Bsize) //nolint:unconvert // platform-dependent type
	return int64(st.Bavail) * bsize, (int64(st.Blocks) - int64(st.Bfree)) * bsize, nil
}

// DiskFree returns the available kilobytes and the used percentage of the
// filesystem holding path, the way `df -k` reports them.
func DiskFree(path string) (availKB int64, usedPercent int, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	bsize := int64(st.Bsize) //nolint:unconvert // Bsize is uint32 on darwin, int64 on linux
	availKB = int64(st.Bavail) * bsize / 1024
	used := int64(st.Blocks) - int64(st.Bfree)
	total := used + int64(st.Bavail)
	if total > 0 {
		// df rounds the percentage up.
		usedPercent = int((used*100 + total - 1) / total)
	}
	return availKB, usedPercent, nil
}
