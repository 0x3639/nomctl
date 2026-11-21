package scheduleproof

import (
	"io/fs"
	"path/filepath"
	"runtime"

	"golang.org/x/sys/unix"
)

type Resources struct {
	CPUSeconds   float64 `json:"processCPUSeconds"`
	PeakRSSBytes int64   `json:"peakRSSBytes"`
	HeapBytes    uint64  `json:"heapAllocatedBytes"`
	DataBytes    int64   `json:"replicaLogicalFileBytes"`
}

func Measure(root string) (Resources, error) {
	var ru unix.Rusage
	if err := unix.Getrusage(unix.RUSAGE_SELF, &ru); err != nil {
		return Resources{}, err
	}
	peak := ru.Maxrss
	if runtime.GOOS != "darwin" {
		peak *= 1024
	}
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	r := Resources{CPUSeconds: float64(ru.Utime.Sec+ru.Stime.Sec) + float64(ru.Utime.Usec+ru.Stime.Usec)/1e6, PeakRSSBytes: peak, HeapBytes: mem.Alloc}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			info, e := d.Info()
			if e != nil {
				return e
			}
			r.DataBytes += info.Size()
		}
		return nil
	})
	return r, err
}
