//go:build unix

package fsx

import "syscall"

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
