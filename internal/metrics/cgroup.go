package metrics

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// DefaultCgroupRoot is the cgroup v2 mount point.
const DefaultCgroupRoot = "/sys/fs/cgroup"

// CgroupStats is what nomctl reads from the service's cgroup. MemoryMax is 0
// when unlimited.
type CgroupStats struct {
	Present       bool
	MemoryCurrent uint64
	MemoryPeak    uint64
	MemoryMax     uint64
	PidsCurrent   int
}

// ReadCgroup reads the cgroup files under cgroupRoot+controlGroup.
func ReadCgroup(cgroupRoot, controlGroup string) CgroupStats {
	var c CgroupStats
	if controlGroup == "" {
		return c
	}
	dir := filepath.Join(cgroupRoot, controlGroup)
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return c
	}
	c.Present = true
	c.MemoryCurrent = cgroupUint(dir, "memory.current")
	c.MemoryPeak = cgroupUint(dir, "memory.peak")
	c.MemoryMax = cgroupUint(dir, "memory.max") // "max" parses as 0
	c.PidsCurrent = int(cgroupUint(dir, "pids.current")) //nolint:gosec // small counter
	return c
}

func cgroupUint(dir, file string) uint64 {
	data, err := os.ReadFile(filepath.Join(dir, file))
	if err != nil {
		return 0
	}
	n, _ := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
	return n
}
