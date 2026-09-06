//go:build unix

package fsx

import "syscall"

// DiskFree returns the available kilobytes and the used percentage of the
// filesystem holding path, the way `df -k` reports them.
func DiskFree(path string) (availKB int64, usedPercent int, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	bsize := int64(st.Bsize)
	availKB = int64(st.Bavail) * bsize / 1024
	used := int64(st.Blocks) - int64(st.Bfree)
	total := used + int64(st.Bavail)
	if total > 0 {
		// df rounds the percentage up.
		usedPercent = int((used*100 + total - 1) / total)
	}
	return availKB, usedPercent, nil
}
