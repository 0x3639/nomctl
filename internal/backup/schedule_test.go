package backup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/lock"
)

func writeTestSchedule(t *testing.T, path string, s Schedule) {
	t.Helper()
	content, err := renderSchedule(s)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyScheduleAdoptionAndStrictParsing(t *testing.T) {
	cfg := testConfig(t)
	cfg.BackupDir = `/backup/it's 100% $(false) "data"`
	cfg.BackupHour, cfg.BackupRetentionDays = 5, 19
	command, err := cronCommand(cfg, "/opt/nomctl's bin", false)
	if err != nil {
		t.Fatal(err)
	}
	legacy := fmt.Sprintf("# Managed by nomctl: daily local chain backups; retention 19 days.\nSHELL=/bin/sh\nPATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin\n17 5 * * * root %s\n", command)
	path := filepath.Join(t.TempDir(), "cron")
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := readSchedule(path)
	if err != nil || s.Directory != cfg.BackupDir || s.Minute != 17 || s.Version != 0 {
		t.Fatalf("%+v %v", s, err)
	}
	// Pausing a legacy entry adopts every saved setting without running a tool.
	t.Setenv("PATH", t.TempDir())
	now := time.Now().UTC()
	if err := setScheduleEnabled(path, false, now); err != nil {
		t.Fatal(err)
	}
	s, err = readSchedule(path)
	if err != nil || s.Enabled || s.Version != 1 || s.Directory != cfg.BackupDir || s.RetentionDays != 19 || !s.Since.Equal(now) {
		t.Fatalf("%+v %v", s, err)
	}
	data, _ := os.ReadFile(path)
	for _, changed := range []string{strings.Replace(string(data), "# Paused: ", "", 1), string(data) + "* * * * * root unexpected\n", strings.Replace(legacy, "'backup'", "'backup'; false", 1)} {
		if err := os.WriteFile(path, []byte(changed), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := readSchedule(path); err == nil {
			t.Fatal("modified cron accepted")
		}
	}
}

func TestScheduleControlsPreserveDataAndPausedEdits(t *testing.T) {
	cfg := testConfig(t)
	root := t.TempDir()
	path := filepath.Join(root, "cron")
	archive := filepath.Join(cfg.BackupDir, "existing-backup")
	if err := os.WriteFile(archive, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := newSchedule(cfg, "/usr/local/bin/nomctl", "host", time.Now().Add(-time.Hour))
	writeTestSchedule(t, path, s)
	commands := t.TempDir()
	events := filepath.Join(root, "events")
	t.Setenv("SCHEDULE_EVENTS", events)
	stubCommand(t, commands, "systemctl", `
echo "$*" >> "$SCHEDULE_EVENTS"
case "$1" in
 status) [ "$2" = cron.service ] || exit 4 ;;
 show) printf 'LoadState=loaded\nActiveState=active\n' ;;
 enable) ;;
 *) exit 1 ;;
esac
`)
	t.Setenv("PATH", commands)
	now := time.Now()
	for range 2 {
		if err := setScheduleEnabled(path, false, now); err != nil {
			t.Fatal(err)
		}
	}
	paused, _ := os.ReadFile(path)
	hour, retention, destination := 23, 30, filepath.Join(root, "new backups")
	if err := editSchedule(path, ScheduleChanges{Hour: &hour, RetentionDays: &retention, Directory: &destination}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(events); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("pause/edit called service tools")
	}
	saved, err := readSchedule(path)
	if err != nil || saved.Enabled || saved.Hour != 23 || saved.Minute != s.Minute || saved.RetentionDays != 30 || saved.Directory != destination || saved.DataDirectory != s.DataDirectory || saved.Executable != s.Executable {
		t.Fatalf("%+v %v", saved, err)
	}
	before, _ := os.ReadFile(path)
	bad := 31
	if err := editSchedule(path, ScheduleChanges{RetentionDays: &bad}, now); err == nil {
		t.Fatal("invalid edit accepted")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("failed edit changed schedule")
	}
	for range 2 {
		if err := setScheduleEnabled(path, true, now); err != nil {
			t.Fatal(err)
		}
	}
	saved, err = readSchedule(path)
	if err != nil || !saved.Enabled {
		t.Fatalf("%+v %v", saved, err)
	}
	data, _ := os.ReadFile(archive)
	if string(data) != "untouched" {
		t.Fatal("schedule controls changed backup")
	}
	calls, _ := os.ReadFile(events)
	if strings.Contains(string(calls), cfg.ServiceName) {
		t.Fatal("schedule controls touched znnd")
	}
	// Failed resume preserves the paused job byte-for-byte.
	if err := os.WriteFile(path, paused, 0o644); err != nil {
		t.Fatal(err)
	}
	stubCommand(t, commands, "systemctl", "exit 1\n")
	if err := setScheduleEnabled(path, true, now); err == nil {
		t.Fatal("resume succeeded despite scheduler error")
	}
	after, _ = os.ReadFile(path)
	if string(after) != string(paused) {
		t.Fatal("failed resume changed paused schedule")
	}
}

func TestScheduledOutcomesAndOperationLock(t *testing.T) {
	for _, outcome := range []string{"success", "failure", "skipped-because-busy", "paused", "history-unwritable"} {
		t.Run(outcome, func(t *testing.T) {
			root := t.TempDir()
			path, history, lockPath := filepath.Join(root, "cron"), filepath.Join(root, "history"), filepath.Join(root, "op.lock")
			s := newSchedule(testConfig(t), "/bin/nomctl", "host", time.Now().Add(-time.Hour))
			if outcome == "paused" {
				s.Enabled = false
			}
			writeTestSchedule(t, path, s)
			if outcome == "skipped-because-busy" {
				l, err := lock.Acquire(lockPath, "restore")
				if err != nil {
					t.Fatal(err)
				}
				defer l.Release()
			}
			if outcome == "history-unwritable" {
				if err := os.WriteFile(history, []byte("file"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			err := runScheduled(path, history, lockPath, func(cfg config.Config, _ Options) (string, error) {
				calls++
				if cfg.BackupDir != s.Directory || cfg.MaxBackups != 30 || cfg.BackupCadenceDays != 0 {
					t.Fatal("saved settings lost")
				}
				if l, err := lock.Acquire(lockPath, "other"); err == nil {
					l.Release()
					t.Fatal("backup ran without operation lock")
				} else if !errors.Is(err, lock.ErrBusy) {
					t.Fatal(err)
				}
				status, err := scheduleStatus(path, history, time.Now())
				if err != nil || status.LastAttempt.Outcome != "running" {
					t.Fatalf("running status: %+v %v", status, err)
				}
				if outcome == "failure" {
					return "", errors.New("insufficient disk space")
				}
				return "/saved/archive.tar.gz", nil
			})
			if (err == nil) != (outcome == "success" || outcome == "paused") {
				t.Fatal(err)
			}
			if outcome == "success" || outcome == "failure" {
				if calls != 1 {
					t.Fatal(calls)
				}
			} else if calls != 0 {
				t.Fatal("unexpected backup")
			}
			if outcome == "history-unwritable" {
				return
			}
			status, err := scheduleStatus(path, history, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if outcome == "paused" {
				if status.LastAttempt != nil {
					t.Fatal("paused invocation recorded as run")
				}
				return
			}
			if status.LastAttempt == nil || status.LastAttempt.Outcome != outcome || status.LastAttempt.Finished == nil {
				t.Fatalf("%+v", status)
			}
			if (status.LastSuccess != nil) != (outcome == "success") {
				t.Fatalf("%+v", status)
			}
			if outcome == "failure" && !strings.Contains(status.LastAttempt.Reason, "insufficient disk") {
				t.Fatal("failure reason lost")
			}
		})
	}
}

func TestStatusMissedInterruptedAndDestinationChanges(t *testing.T) {
	root := t.TempDir()
	path, history := filepath.Join(root, "cron"), filepath.Join(root, "history")
	if err := os.Mkdir(history, 0o700); err != nil {
		t.Fatal(err)
	}
	loc, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 16, 8, 0, 0, 0, loc)
	s := newSchedule(testConfig(t), "/bin/nomctl", "host", now.Add(-48*time.Hour))
	s.Hour, s.Minute = 2, 15
	writeTestSchedule(t, path, s)
	status, err := scheduleStatus(path, history, now)
	if err != nil || status.MissedRun == nil || status.MissedRun.Hour() != 2 || status.NextRun.Day() != 17 || status.LastSuccess != nil {
		t.Fatalf("%+v %v", status, err)
	}
	if !strings.Contains(status.Timezone, "America/Chicago") {
		t.Fatal(status.Timezone)
	}
	r := ScheduledRun{Started: now.Add(-5 * time.Hour), Outcome: "running", PID: -1, ProcessIdentity: "previous-boot", Directory: s.Directory, DataDirectory: s.DataDirectory, Service: s.Service, ScheduleSince: s.Since}
	if err := saveRun(filepath.Join(history, "run.json"), r); err != nil {
		t.Fatal(err)
	}
	status, err = scheduleStatus(path, history, now)
	if err != nil || status.LastAttempt.Outcome != "interrupted" || status.MissedRun != nil {
		t.Fatalf("%+v %v", status, err)
	}
	s.Enabled = false
	writeTestSchedule(t, path, s)
	status, _ = scheduleStatus(path, history, now)
	if status.NextRun != nil || status.MissedRun != nil {
		t.Fatal("paused status reports scheduled/missed slot")
	}
	s.Enabled, s.Since = true, now
	writeTestSchedule(t, path, s)
	status, _ = scheduleStatus(path, history, now)
	if status.MissedRun != nil {
		t.Fatal("resume reported paused period as missed")
	}
	s.Directory = "/new-destination"
	writeTestSchedule(t, path, s)
	status, _ = scheduleStatus(path, history, now)
	if status.LastAttempt != nil {
		t.Fatal("old destination claimed as current")
	}
}

func TestDailySlotsDSTAndGrace(t *testing.T) {
	loc, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Fatal(err)
	}
	s := Schedule{Hour: 2, Minute: 30}
	now := time.Date(2026, 3, 7, 3, 0, 0, 0, loc)
	if got := dailySlot(s, now, true); got.Day() != 8 || got.Hour() != 3 || got.Minute() != 0 {
		t.Fatal(got)
	}
	s.Hour = 1
	first := time.Date(2026, 11, 1, 1, 30, 0, 0, loc)
	second := dailySlot(s, first, true)
	if second.Day() != 2 || second.Hour() != 1 || second.Sub(first) != 25*time.Hour {
		t.Fatal(first, second)
	}
	root := t.TempDir()
	path := filepath.Join(root, "cron")
	s = newSchedule(testConfig(t), "/bin/nomctl", "host", time.Date(2026, 11, 15, 12, 0, 0, 0, loc))
	s.Hour, s.Minute = 2, 30
	writeTestSchedule(t, path, s)
	status, err := scheduleStatus(path, filepath.Join(root, "history"), time.Date(2026, 11, 16, 2, 32, 0, 0, loc))
	if err != nil || status.MissedRun != nil {
		t.Fatalf("grace: %+v %v", status, err)
	}
}

func TestHistoryBoundedAndLastSuccessRetained(t *testing.T) {
	dir := t.TempDir()
	start := time.Now().Add(-100 * time.Hour)
	for i := range 70 {
		r := ScheduledRun{Started: start.Add(time.Duration(i) * time.Hour), Outcome: "failure"}
		if i == 0 {
			r.Outcome = "success"
		}
		if err := saveRun(filepath.Join(dir, fmt.Sprintf("%d.json", i)), r); err != nil {
			t.Fatal(err)
		}
	}
	if err := pruneHistory(dir); err != nil {
		t.Fatal(err)
	}
	records, err := readHistory(dir)
	if err != nil || len(records) != 65 || records[64].run.Outcome != "success" {
		t.Fatalf("count=%d err=%v", len(records), err)
	}
}
