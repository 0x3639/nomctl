package recovery

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/lock"
)

func scheduleFixture(t *testing.T, cfg config.Config) (string, Schedule) {
	t.Helper()
	s := Schedule{Version: 1, Enabled: true, Since: time.Now().UTC(), Hour: 5, Directory: cfg.BackupDir, Service: cfg.ServiceName, Executable: "/usr/local/bin/nomctl", LogFile: cfg.LogFile}
	path := filepath.Join(t.TempDir(), "nomctl-verify")
	content, err := renderSchedule(s)
	if err != nil {
		t.Fatal(err)
	}
	write(t, path, []byte(content))
	return path, s
}

func TestSchedulePublicationPreservesPauseAndRejectsStaleEdits(t *testing.T) {
	cfg, _ := fixture(t, false)
	path, s := scheduleFixture(t, cfg)
	old := ensureCron
	t.Cleanup(func() { ensureCron = old })
	calls := 0
	ensureCron = func() error { calls++; return nil }
	s.Enabled = false
	if err := publishSchedule(path, s, &s); err == nil {
		t.Fatal("accepted stale expected state")
	}
	original, err := readSchedule(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := publishSchedule(path, s, original); err != nil {
		t.Fatal(err)
	}
	paused, err := readSchedule(path)
	if err != nil || paused.Enabled || calls != 0 {
		t.Fatal(paused, calls, err)
	}
	edited := *paused
	edited.Hour = 8
	if err := publishSchedule(path, edited, paused); err != nil {
		t.Fatal(err)
	}
	current, err := readSchedule(path)
	if err != nil || current.Enabled || current.Hour != 8 || calls != 0 {
		t.Fatal(current, calls, err)
	}
	resume := *current
	resume.Enabled = true
	ensureCron = func() error { return errors.New("scheduler unavailable") }
	if err := publishSchedule(path, resume, current); err == nil {
		t.Fatal("ignored readiness failure")
	}
	still, err := readSchedule(path)
	if err != nil || *still != *current {
		t.Fatal("changed schedule after failure", still, err)
	}
	ensureCron = func() error { calls++; return nil }
	if err := publishSchedule(path, resume, current); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal(calls)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(content), " root ") != 1 {
		t.Fatal(string(content))
	}
	write(t, path, append(content, []byte("0 0 * * * root unknown-command\n")...))
	if _, err := readSchedule(path); err == nil {
		t.Fatal("accepted altered cron")
	}
}

func TestScheduleEscapingAndValidation(t *testing.T) {
	cfg, _ := fixture(t, false)
	_, s := scheduleFixture(t, cfg)
	s.Executable = "/opt/nomctl with '% chars"
	s.Directory = "/backups with ' quotes %"
	s.LogFile = "/var/log/with ' quotes %"
	content, err := renderSchedule(s)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, `'"'"'`) || !strings.Contains(content, `\%`) || !strings.Contains(content, "'--scheduled'") {
		t.Fatal(content)
	}
	for _, bad := range []Schedule{
		{Version: 1, Since: time.Now(), Hour: 24, Directory: "/backup", Service: "go-zenon", Executable: "/bin/nomctl"},
		{Version: 1, Since: time.Now(), Hour: 5, Directory: "relative", Service: "go-zenon", Executable: "/bin/nomctl"},
		{Version: 1, Since: time.Now(), Hour: 5, Directory: "/backup", Service: "go-zenon", Executable: "/bin/bad\ncommand"},
	} {
		if _, err := renderSchedule(bad); err == nil {
			t.Fatal("accepted bad schedule", bad)
		}
	}
}

func TestScheduledVerificationBusyPauseAndGeneration(t *testing.T) {
	cfg, target := fixture(t, false)
	path, s := scheduleFixture(t, cfg)
	stateDir, lockPath := t.TempDir(), filepath.Join(t.TempDir(), "lock")
	l, err := lock.Acquire(lockPath, "node upgrade")
	if err != nil {
		t.Fatal(err)
	}
	_, err = runScheduled(context.Background(), path, stateDir, lockPath)
	l.Release()
	if !errors.Is(err, lock.ErrBusy) {
		t.Fatal(err)
	}
	a, err := LastAttempt(stateDir, s)
	if err != nil || a.Outcome != "skipped-because-busy" {
		t.Fatal(a, err)
	}
	if saved := SavedStatus(context.Background(), stateDir, target); saved.State != "never-verified" {
		t.Fatal(saved)
	}
	report, err := runScheduled(context.Background(), path, stateDir, lockPath)
	if err != nil || len(report.Results) != 1 || report.Results[0].Rehearsal != "not_requested" {
		t.Fatal(report, err)
	}
	a, err = LastAttempt(stateDir, s)
	if err != nil || a.Outcome != "passed" {
		t.Fatal(a, err)
	}
	s.Enabled = false
	content, err := renderSchedule(s)
	if err != nil {
		t.Fatal(err)
	}
	write(t, path, []byte(content))
	paused, err := runScheduled(context.Background(), path, stateDir, lockPath)
	if err != nil || len(paused.Results) != 0 {
		t.Fatal(paused, err)
	}
	// An invocation that saw the old generation must not run after pause/resume.
	observed := s
	observed.Enabled = true
	s.Enabled = true
	s.Since = s.Since.Add(time.Second)
	content, err = renderSchedule(s)
	if err != nil {
		t.Fatal(err)
	}
	write(t, path, []byte(content))
	stale, err := scheduledAttempt(context.Background(), observed, path, stateDir, lockPath)
	if err != nil || len(stale.Results) != 0 {
		t.Fatal(stale, err)
	}
	write(t, target.Path, []byte("corrupted"))
	if _, err := runScheduled(context.Background(), path, stateDir, lockPath); err == nil {
		t.Fatal("accepted damaged backup")
	}
	a, err = LastAttempt(stateDir, s)
	if err != nil || a.Outcome != "failed" || a.Finished.IsZero() {
		t.Fatal(a, err)
	}
	if saved := SavedStatus(context.Background(), stateDir, target); saved.State != "failed" {
		t.Fatal(saved)
	}
}
