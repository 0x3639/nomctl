package metrics

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// Probe is what the root-run `nomctl alerts probe` records for the daemon:
// the per-process figures that reading another user's /proc requires
// CAP_SYS_PTRACE for. The unprivileged daemon reads this file instead.
type Probe struct {
	At  time.Time `json:"at"`
	PID int       `json:"pid"` // 0 when the node process is not running
	// StartTime is the process's start (clock ticks after boot) so a pid
	// reused within ProbeMaxAge is not mistaken for the same process.
	StartTime  uint64 `json:"start_time"`
	OpenFDs    int    `json:"open_fds"`
	FDLimit    uint64 `json:"fd_limit"`
	ReadBytes  uint64 `json:"read_bytes"`
	WriteBytes uint64 `json:"write_bytes"`
	// Disk free/total of the data directory in bytes, for a data directory
	// on a mount the daemon cannot see (ProtectHome hides /root). DiskOK
	// is false when the measurement failed; consumers must ignore the
	// figures then.
	DiskOK    bool   `json:"disk_ok"`
	DiskFree  uint64 `json:"disk_free"`
	DiskTotal uint64 `json:"disk_total"`
}

// DefaultProbePath is where the probe timer writes and the daemon reads.
const DefaultProbePath = "/run/nomctl/process-probe.json"

// ProbeMaxAge is how old a probe may be before the daemon ignores it: the
// timer runs every 30 s, so two minutes means four missed runs.
const ProbeMaxAge = 2 * time.Minute

// TakeProbe reads the per-process figures for pid (0 = not running) from
// /proc and the disk figures for dataDir.
func TakeProbe(procRoot string, pid int, dataDir string, now time.Time) (Probe, error) {
	p := Probe{At: now, PID: pid}
	if free, total, err := diskFree(dataDir); err == nil && total > 0 {
		p.DiskOK, p.DiskFree, p.DiskTotal = true, free, total
	}
	if pid == 0 {
		return p, nil
	}
	if st, err := ReadProcStat(procRoot, pid); err == nil {
		p.StartTime = st.StartTime
	}
	fds, err := CountFDs(procRoot, pid)
	if err != nil {
		return p, err
	}
	p.OpenFDs = fds
	p.FDLimit, _ = ReadFDLimit(procRoot, pid)
	if pio, err := ReadProcIO(procRoot, pid); err == nil {
		p.ReadBytes, p.WriteBytes = pio.ReadBytes, pio.WriteBytes
	}
	return p, nil
}

// WriteProbe stores p at path atomically. The directory may belong to the
// daemon's user: the temp file is created O_EXCL with a random name, and
// rename replaces whatever is at path, link or not.
func WriteProbe(path string, p Probe) error {
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".probe-")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return err
	}
	return nil
}

// ReadProbe loads a fresh probe from path. Callers using the process
// fields must check PID themselves (see ForPID).
func ReadProbe(path string, now time.Time) (Probe, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Probe{}, err
	}
	var p Probe
	if err := json.Unmarshal(data, &p); err != nil {
		return Probe{}, err
	}
	if age := now.Sub(p.At); age > ProbeMaxAge || age < -ProbeMaxAge {
		return Probe{}, errors.New("probe is stale")
	}
	return p, nil
}

// ForProcess reports whether the probe's process fields describe the
// process instance (pid, start time).
func (p Probe) ForProcess(pid int, startTime uint64) bool {
	return pid != 0 && p.PID == pid && p.StartTime == startTime
}
