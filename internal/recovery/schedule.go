package recovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/0x3639/nomctl/internal/backup"
	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/fsx"
	"github.com/0x3639/nomctl/internal/lock"
)

const CronPath = "/etc/cron.d/nomctl-verify"
const marker = "# nomctl-verification: "

type Schedule struct {
	Version    int       `json:"version"`
	Enabled    bool      `json:"enabled"`
	Since      time.Time `json:"since"`
	Hour       int       `json:"hour"`
	Directory  string    `json:"directory"`
	Service    string    `json:"service"`
	Executable string    `json:"executable"`
	LogFile    string    `json:"log_file"`
}

func (s Schedule) Config() config.Config {
	c := config.Default()
	c.BackupDir, c.ServiceName, c.LogFile = s.Directory, s.Service, s.LogFile
	return c
}

func renderSchedule(s Schedule) (string, error) {
	if s.Version != 1 || s.Since.IsZero() || s.Hour < 0 || s.Hour > 23 || !filepath.IsAbs(s.Directory) || !filepath.IsAbs(s.Executable) {
		return "", errors.New("invalid verification schedule settings")
	}
	if err := s.Config().Validate(); err != nil {
		return "", err
	}
	args := []string{"/usr/bin/env", "-i", "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "HOME=/root",
		"NOMCTL_LOG_FILE=" + s.LogFile, s.Executable, "verify", "--scheduled"}
	for i, arg := range args {
		if strings.ContainsAny(arg, "\r\n\x00") {
			return "", errors.New("verification schedule cannot contain newlines or NUL")
		}
		args[i] = "'" + strings.ReplaceAll(arg, "'", "'\"'\"'") + "'"
	}
	// Validate settings even when they are read from metadata by --scheduled.
	for _, value := range []string{s.Directory, s.Service, s.LogFile} {
		if strings.ContainsAny(value, "\r\n\x00") {
			return "", errors.New("verification settings cannot contain newlines or NUL")
		}
	}
	data, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	paused := ""
	if !s.Enabled {
		paused = "# Paused: "
	}
	return fmt.Sprintf("# Managed by nomctl: daily recovery-file verification.\n%s%s\nSHELL=/bin/sh\nPATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin\n%s17 %d * * * root %s\n", marker, data, paused, s.Hour, strings.ReplaceAll(strings.Join(args, " "), "%", `\%`)), nil
}

func ReadSchedule() (*Schedule, error) { return readSchedule(CronPath) }

func readSchedule(path string) (*Schedule, error) {
	data, err := fsx.ReadRuntimeFile(path, 64<<10)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, marker) {
			continue
		}
		var s Schedule
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, marker)), &s); err != nil {
			return nil, err
		}
		expected, err := renderSchedule(s)
		if err != nil {
			return nil, err
		}
		if expected != string(data) {
			return nil, errors.New("verification cron file differs from its saved settings; review it before scheduling again")
		}
		return &s, nil
	}
	return nil, errors.New("unrecognized verification cron file")
}

var ensureCron = backup.EnsureCronReady

// SaveSchedule must run under the shared lock. expected prevents a stale menu
// form from overwriting another operator's edit or pause.
func SaveSchedule(cfg config.Config, hour int, expected *Schedule) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	s := Schedule{Version: 1, Enabled: true, Hour: hour, Directory: cfg.BackupDir, Service: cfg.ServiceName, LogFile: cfg.LogFile, Executable: executable}
	if expected != nil {
		s.Enabled = expected.Enabled
	}
	return publishSchedule(CronPath, s, expected)
}

func publishSchedule(path string, s Schedule, expected *Schedule) error {
	current, err := readSchedule(path)
	if err != nil {
		return err
	}
	if (current == nil) != (expected == nil) || (current != nil && *current != *expected) {
		return errors.New("verification schedule changed; reopen its settings")
	}
	s.Since = time.Now().UTC()
	if _, err := renderSchedule(s); err != nil {
		return err
	}
	if s.Enabled {
		if err := ensureCron(); err != nil {
			return err
		}
	}
	s.Since = time.Now().UTC()
	content, err := renderSchedule(s)
	if err != nil {
		return err
	}
	// /etc/cron.d is normally 0755 and owned by root. Atomic replacement never
	// follows a final link; the destination must remain a managed schedule.
	return fsx.WriteRuntimeFile(path, []byte(content), 0o644)
}

func SetScheduleEnabled(enabled bool, expected *Schedule) error {
	if expected == nil {
		return errors.New("no verification schedule is configured")
	}
	s := *expected
	s.Enabled = enabled
	return publishSchedule(CronPath, s, expected)
}

type ScheduledAttempt struct {
	Started  time.Time `json:"started"`
	Finished time.Time `json:"finished"`
	Outcome  string    `json:"outcome"`
	Reason   string    `json:"reason,omitempty"`
	Schedule Schedule  `json:"schedule"`
}

func attemptPath(dir string, s Schedule) string {
	return filepath.Join(dir, "scheduled-"+key([]string{s.Directory, s.Service})+".json")
}

func LastAttempt(dir string, s Schedule) (*ScheduledAttempt, error) {
	data, err := fsx.ReadRuntimeFile(attemptPath(dir, s), 64<<10)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var a ScheduledAttempt
	if err := json.Unmarshal(data, &a); err != nil {
		return nil, err
	}
	if a.Outcome == "running" {
		a.Outcome = "unfinished"
		a.Reason = "attempt has no saved completion"
	}
	return &a, nil
}

func RunScheduled(ctx context.Context, stateDir, lockPath string) (Report, error) {
	return runScheduled(ctx, CronPath, stateDir, lockPath)
}

func runScheduled(ctx context.Context, schedulePath, stateDir, lockPath string) (report Report, err error) {
	s, err := readSchedule(schedulePath)
	if err != nil {
		return report, err
	}
	if s == nil {
		return report, errors.New("verification schedule is not configured")
	}
	if !s.Enabled {
		return Report{Limits: Limits}, nil
	}
	return scheduledAttempt(ctx, *s, schedulePath, stateDir, lockPath)
}

func scheduledAttempt(ctx context.Context, observed Schedule, schedulePath, stateDir, lockPath string) (report Report, err error) {
	a := ScheduledAttempt{Started: time.Now().UTC(), Outcome: "running", Schedule: observed}
	l, lockErr := lock.Acquire(lockPath, "scheduled recovery verification")
	if lockErr == nil {
		defer l.Release()
		s, readErr := readSchedule(schedulePath)
		if readErr != nil {
			return report, readErr
		}
		if s == nil || !s.Enabled || *s != observed {
			return Report{Limits: Limits}, nil
		}
	}
	defer func() {
		a.Finished = time.Now().UTC()
		if err != nil {
			a.Reason = err.Error()
			if len(a.Reason) > 4096 {
				a.Reason = a.Reason[:4096]
			}
		}
		err = errors.Join(err, saveJSON(attemptPath(stateDir, observed), a))
	}()
	if lockErr != nil {
		a.Outcome = "failed"
		if errors.Is(lockErr, lock.ErrBusy) {
			a.Outcome = "skipped-because-busy"
		}
		return Report{Limits: Limits}, lockErr
	}
	if err = saveJSON(attemptPath(stateDir, observed), a); err != nil {
		a.Outcome = "failed"
		return report, err
	}
	targets, err := List(observed.Config())
	if err == nil {
		report, err = runLocked(ctx, observed.Config(), targets, Options{}, stateDir)
	}
	a.Outcome = "passed"
	if err != nil {
		a.Outcome = "failed"
	}
	return report, err
}

func WriteSchedule(w io.Writer, s *Schedule, a *ScheduledAttempt) {
	if s == nil {
		fmt.Fprintln(w, "Daily verification: not configured")
		return
	}
	state := "enabled"
	if !s.Enabled {
		state = "paused"
	}
	fmt.Fprintf(w, "Daily verification: %s at %02d:17 (server time)\nDirectory: %s\nService: %s\n", state, s.Hour, s.Directory, s.Service)
	fmt.Fprintln(w, "Busy runs are skipped; no catch-up or automatic retries. The next daily slot is the next attempt.")
	if a != nil {
		fmt.Fprintf(w, "Last scheduled attempt: %s at %s\n%s\n", a.Outcome, a.Started.Format(time.RFC3339), a.Reason)
	}
}
