package backup

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/lock"
)

const HistoryDir = "/var/lib/nomctl/backups"

// ScheduledRun describes a scheduled attempt; manual saves and protected
// restore points never update this history.
type ScheduledRun struct {
	Started         time.Time  `json:"started"`
	Finished        *time.Time `json:"finished,omitempty"`
	Outcome         string     `json:"outcome"`
	Reason          string     `json:"reason,omitempty"`
	Archive         string     `json:"archive,omitempty"`
	Directory       string     `json:"directory"`
	DataDirectory   string     `json:"data_directory"`
	Service         string     `json:"service"`
	ScheduleSince   time.Time  `json:"schedule_since"`
	PID             int        `json:"pid"`
	ProcessIdentity string     `json:"process_identity"`
}

// RunScheduled owns the operation lock and checks saved settings again after
// acquiring it. A cron process queued before pause cannot start a backup later.
func RunScheduled(lockPath string) error {
	return runScheduled(CronPath, HistoryDir, lockPath, Run)
}

func runScheduled(schedulePath, historyDir, lockPath string, run func(config.Config, Options) (string, error)) error {
	s, err := requiredSchedule(schedulePath)
	if err != nil {
		return err
	}
	if !s.Enabled {
		return nil
	}
	r := ScheduledRun{Started: time.Now().UTC(), Outcome: "running", PID: os.Getpid()}
	r.ProcessIdentity, err = processIdentity(r.PID)
	if err != nil {
		return fmt.Errorf("identify scheduled backup process: %w", err)
	}
	l, lockErr := lock.Acquire(lockPath, "scheduled backup")
	if lockErr == nil {
		defer l.Release()
		s, err = requiredSchedule(schedulePath)
		if err != nil {
			return err
		}
		if !s.Enabled {
			return nil
		}
	}
	r.Directory, r.Service, r.ScheduleSince = s.Directory, s.Service, s.Since
	r.DataDirectory = s.DataDirectory
	if err := os.MkdirAll(historyDir, 0o700); err != nil {
		return fmt.Errorf("create backup history: %w", err)
	}
	path := filepath.Join(historyDir, fmt.Sprintf("%020d-%d.json", r.Started.UnixNano(), r.PID))
	if lockErr != nil {
		r.Outcome, r.Reason = "failure", lockErr.Error()
		if errors.Is(lockErr, lock.ErrBusy) {
			r.Outcome = "skipped-because-busy"
		}
		now := time.Now().UTC()
		r.Finished = &now
		return errors.Join(lockErr, finishRun(path, r))
	}
	if err := saveRun(path, r); err != nil {
		return err
	}
	r.Archive, err = run(s.Config(), Options{})
	r.Outcome = "success"
	if err != nil {
		r.Outcome, r.Reason = "failure", err.Error()
	} else if r.Archive == "" {
		r.Outcome, r.Reason = "failure", "scheduled backup returned no archive"
		err = errors.New(r.Reason)
	}
	now := time.Now().UTC()
	r.Finished = &now
	return errors.Join(err, finishRun(path, r))
}

func finishRun(path string, r ScheduledRun) error {
	if err := saveRun(path, r); err != nil {
		return err
	}
	return pruneHistory(filepath.Dir(path))
}

func saveRun(path string, r ScheduledRun) error {
	// External tools may produce large errors. Bound persisted diagnostic text.
	if len(r.Reason) > 4096 {
		r.Reason = r.Reason[:4096] + " (truncated; see nomctl log)"
	}
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if err := atomicWrite(path, data, 0o600); err != nil {
		return fmt.Errorf("save backup outcome: %w", err)
	}
	return nil
}

// Linux process start ticks plus boot ID prevent a reused PID, including after
// reboot, from making an interrupted attempt look like it is still running.
func processIdentity(pid int) (string, error) {
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", err
	}
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return "", err
	}
	end := strings.LastIndexByte(string(stat), ')')
	if end < 0 {
		return "", errors.New("invalid process stat")
	}
	fields := strings.Fields(string(stat[end+1:]))
	if len(fields) < 20 {
		return "", errors.New("incomplete process stat")
	}
	return strings.TrimSpace(string(boot)) + ":" + fields[19], nil
}

type storedRun struct {
	path string
	run  ScheduledRun
}

func readHistory(dir string) ([]storedRun, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var records []storedRun
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		} // concurrent retention
		if err != nil {
			return nil, err
		}
		var r ScheduledRun
		if err := json.Unmarshal(data, &r); err != nil {
			return nil, fmt.Errorf("read backup history %s: %w", path, err)
		}
		if r.Started.IsZero() {
			return nil, fmt.Errorf("invalid backup history: %s", path)
		}
		if r.Outcome == "running" {
			identity, err := processIdentity(r.PID)
			if err != nil || identity != r.ProcessIdentity {
				r.Outcome, r.Reason = "interrupted", "process exited or the server restarted before an outcome was saved; inspect the nomctl log and node status"
			}
		}
		records = append(records, storedRun{path, r})
	}
	sort.Slice(records, func(i, j int) bool { return records[i].run.Started.After(records[j].run.Started) })
	return records, nil
}

// Keep 64 recent attempts plus the most recent success. Never prune an active
// writer. These are metadata files only, not backup archives or restore points.
func pruneHistory(dir string) error {
	records, err := readHistory(dir)
	if err != nil {
		return err
	}
	keptSuccess := false
	for i, r := range records {
		preserve := r.run.Outcome == "success" && !keptSuccess
		if r.run.Outcome == "success" {
			keptSuccess = true
		}
		if i < 64 || preserve || r.run.Outcome == "running" {
			continue
		}
		if err := os.Remove(r.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}
