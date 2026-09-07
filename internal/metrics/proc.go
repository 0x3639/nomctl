// Package metrics samples the node process, its host and its RPC into one
// Sample used by nomctl status, nomctl top and the support bundle.
package metrics

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// DefaultProcRoot is where /proc is mounted.
const DefaultProcRoot = "/proc"

// ProcStatus is the subset of /proc/<pid>/status nomctl uses; sizes in bytes.
type ProcStatus struct {
	Name    string
	State   string
	Threads int
	FDSize  int
	VmRSS   uint64
	VmSwap  uint64
	VmPeak  uint64
}

func pidPath(root string, pid int, file string) string {
	return filepath.Join(root, strconv.Itoa(pid), file)
}

// ReadProcStatus parses /proc/<pid>/status.
func ReadProcStatus(root string, pid int) (ProcStatus, error) {
	var st ProcStatus
	f, err := os.Open(pidPath(root, pid, "status"))
	if err != nil {
		return st, err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		key, val, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		val = strings.TrimSpace(val)
		switch key {
		case "Name":
			st.Name = val
		case "State":
			st.State = val
		case "Threads":
			st.Threads, _ = strconv.Atoi(val)
		case "FDSize":
			st.FDSize, _ = strconv.Atoi(val)
		case "VmRSS":
			st.VmRSS = kbField(val)
		case "VmSwap":
			st.VmSwap = kbField(val)
		case "VmPeak":
			st.VmPeak = kbField(val)
		}
	}
	return st, sc.Err()
}

// kbField parses "1234 kB" into bytes.
func kbField(s string) uint64 {
	n, _ := strconv.ParseUint(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "kB")), 10, 64)
	return n * 1024
}

// ProcStat is the CPU part of /proc/<pid>/stat, in clock ticks.
type ProcStat struct {
	UTime uint64
	STime uint64
	// StartTime is field 22: clock ticks after boot when the process
	// started. With the pid it identifies one process instance.
	StartTime uint64
}

// ReadProcStat parses fields 14, 15 and 22 of /proc/<pid>/stat.
func ReadProcStat(root string, pid int) (ProcStat, error) {
	data, err := os.ReadFile(pidPath(root, pid, "stat"))
	if err != nil {
		return ProcStat{}, err
	}
	// The command name is in parentheses and may contain spaces; skip past it.
	s := string(data)
	i := strings.LastIndex(s, ")")
	if i < 0 {
		return ProcStat{}, errors.New("malformed stat")
	}
	fields := strings.Fields(s[i+1:])
	// fields[0] is state (field 3); utime is field 14 -> index 11.
	if len(fields) < 20 {
		return ProcStat{}, errors.New("malformed stat")
	}
	u, _ := strconv.ParseUint(fields[11], 10, 64)
	st, _ := strconv.ParseUint(fields[12], 10, 64)
	start, _ := strconv.ParseUint(fields[19], 10, 64) // field 22 -> index 19
	return ProcStat{UTime: u, STime: st, StartTime: start}, nil
}

// ProcIO is the storage part of /proc/<pid>/io.
type ProcIO struct {
	ReadBytes  uint64
	WriteBytes uint64
}

// ReadProcIO parses /proc/<pid>/io.
func ReadProcIO(root string, pid int) (ProcIO, error) {
	var pio ProcIO
	f, err := os.Open(pidPath(root, pid, "io"))
	if err != nil {
		return pio, err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		key, val, _ := strings.Cut(sc.Text(), ":")
		n, _ := strconv.ParseUint(strings.TrimSpace(val), 10, 64)
		switch key {
		case "read_bytes":
			pio.ReadBytes = n
		case "write_bytes":
			pio.WriteBytes = n
		}
	}
	return pio, sc.Err()
}

// ReadFDLimit returns the soft "Max open files" limit of the process; 0
// means unlimited.
func ReadFDLimit(root string, pid int) (uint64, error) {
	f, err := os.Open(pidPath(root, pid, "limits"))
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "Max open files") {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(line, "Max open files"))
		if len(fields) < 1 {
			break
		}
		if fields[0] == "unlimited" {
			return 0, nil
		}
		return strconv.ParseUint(fields[0], 10, 64)
	}
	return 0, fmt.Errorf("max open files not found for pid %d", pid)
}

// CountFDs counts entries in /proc/<pid>/fd.
func CountFDs(root string, pid int) (int, error) {
	entries, err := os.ReadDir(pidPath(root, pid, "fd"))
	if err != nil {
		return 0, err
	}
	return len(entries), nil
}

// ReadLoadAvg parses /proc/loadavg.
func ReadLoadAvg(root string) (l1, l5, l15 float64, err error) {
	data, err := os.ReadFile(filepath.Join(root, "loadavg"))
	if err != nil {
		return 0, 0, 0, err
	}
	fields := strings.Fields(string(data))
	if len(fields) < 3 {
		return 0, 0, 0, errors.New("malformed loadavg")
	}
	l1, _ = strconv.ParseFloat(fields[0], 64)
	l5, _ = strconv.ParseFloat(fields[1], 64)
	l15, _ = strconv.ParseFloat(fields[2], 64)
	return l1, l5, l15, nil
}

// MemInfo is the subset of /proc/meminfo nomctl uses, in bytes.
type MemInfo struct {
	Total     uint64
	Available uint64
}

// ReadMemInfo parses /proc/meminfo.
func ReadMemInfo(root string) (MemInfo, error) {
	var m MemInfo
	f, err := os.Open(filepath.Join(root, "meminfo"))
	if err != nil {
		return m, err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		key, val, _ := strings.Cut(sc.Text(), ":")
		switch key {
		case "MemTotal":
			m.Total = kbField(val)
		case "MemAvailable":
			m.Available = kbField(val)
		}
	}
	if m.Total == 0 {
		return m, errors.New("MemTotal not found")
	}
	return m, sc.Err()
}

// Pressure holds the "some avg10" values from /proc/pressure, in percent.
type Pressure struct {
	CPU    float64
	IO     float64
	Memory float64
}

// ReadPressure reads /proc/pressure/{cpu,io,memory}; missing files give 0.
func ReadPressure(root string) Pressure {
	return Pressure{
		CPU:    pressureSomeAvg10(filepath.Join(root, "pressure", "cpu")),
		IO:     pressureSomeAvg10(filepath.Join(root, "pressure", "io")),
		Memory: pressureSomeAvg10(filepath.Join(root, "pressure", "memory")),
	}
}

func pressureSomeAvg10(path string) float64 {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "some ") {
			continue
		}
		for _, kv := range strings.Fields(line)[1:] {
			if v, ok := strings.CutPrefix(kv, "avg10="); ok {
				f, _ := strconv.ParseFloat(v, 64)
				return f
			}
		}
	}
	return 0
}
