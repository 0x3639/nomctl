package backup

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/lock"
)

func readyScheduler(t *testing.T, readyPath string) {
	t.Helper()
	commands := t.TempDir()
	t.Setenv("SCHEDULE_READY", readyPath)
	stubCommand(t, commands, "systemctl", `
case "$1" in
 status) exit 3 ;;
 show) printf 'LoadState=loaded\nActiveState=active\n' ;;
 enable|is-enabled) ;;
 disable) : > "$SCHEDULE_READY" ;;
 *) exit 1 ;;
esac
`)
	t.Setenv("PATH", commands)
}

func TestScheduleEpochBeginsAfterSchedulerReady(t *testing.T) {
	root := t.TempDir()
	path, ready := filepath.Join(root, "cron"), filepath.Join(root, "ready")
	readyScheduler(t, ready)
	started := time.Date(2026, 11, 16, 1, 59, 30, 0, time.UTC)
	published := started.Add(150 * time.Second) // 02:02, after the 02:00 slot
	s := newSchedule(testConfig(t), "/bin/nomctl", "host", started)
	s.Hour, s.Minute = 2, 0
	clock := func() time.Time {
		if _, err := os.Stat(ready); err != nil {
			t.Fatal("epoch stamped before scheduler readiness and timer migration", err)
		}
		return published
	}
	if err := publishSchedule(s, path, true, clock); err != nil {
		t.Fatal(err)
	}
	saved, err := readSchedule(path)
	if err != nil || !saved.Since.Equal(published) {
		t.Fatalf("%+v %v", saved, err)
	}
	status, err := scheduleStatus(path, filepath.Join(root, "history"), published.Add(4*time.Minute))
	if err != nil || status.MissedRun != nil {
		t.Fatalf("reported a missed slot before publication: %+v %v", status, err)
	}
	if err := setScheduleEnabled(path, true, func() time.Time { t.Fatal("idempotent resume reset epoch"); return published }); err != nil {
		t.Fatal(err)
	}
	saved, err = readSchedule(path)
	if err != nil || !saved.Since.Equal(published) {
		t.Fatalf("idempotent resume: %+v %v", saved, err)
	}
	if err := setScheduleEnabled(path, false, func() time.Time { return published.Add(time.Minute) }); err != nil {
		t.Fatal(err)
	}
	if err := setScheduleEnabled(path, false, func() time.Time { t.Fatal("idempotent pause reset epoch"); return published }); err != nil {
		t.Fatal(err)
	}
}

func TestStaleInvocationCannotRunAfterPauseResume(t *testing.T) {
	root := t.TempDir()
	path, history, lockPath := filepath.Join(root, "cron"), filepath.Join(root, "history"), filepath.Join(root, "operation.lock")
	readyScheduler(t, filepath.Join(root, "ready"))
	before := time.Now().Add(-time.Hour).UTC()
	s := newSchedule(testConfig(t), "/bin/nomctl", "host", before)
	writeTestSchedule(t, path, s)
	observed, err := requiredSchedule(path)
	if err != nil {
		t.Fatal(err)
	}
	// The invocation is suspended after its initial read while another operator
	// completes both controls. Then it continues into the real locked path.
	for i, enabled := range []bool{false, true} {
		held, err := lock.Acquire(lockPath, "schedule control")
		if err != nil {
			t.Fatal(err)
		}
		err = setScheduleEnabled(path, enabled, func() time.Time { return before.Add(time.Duration(i+1) * time.Minute) })
		held.Release()
		if err != nil {
			t.Fatal(err)
		}
	}
	calls := 0
	if err := runScheduledAttempt(*observed, path, history, lockPath, func(config.Config, Options) (string, error) {
		calls++
		return "/backup/archive", nil
	}); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatal("stale invocation started a backup after resume")
	}
	if _, err := os.Stat(history); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("stale invocation wrote run history", err)
	}
	held, err := lock.Acquire(lockPath, "next operation")
	if err != nil {
		t.Fatal("stale invocation kept the lock", err)
	}
	held.Release()
}

func TestMenuFormRejectsConcurrentScheduleChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cron")
	if err := checkScheduleUnchanged(path, nil); err != nil {
		t.Fatal(err)
	}
	s := newSchedule(testConfig(t), "/bin/nomctl", "host", time.Now())
	s.Enabled = false
	writeTestSchedule(t, path, s)
	before, _ := os.ReadFile(path)
	// A setup form opened with no schedule may not overwrite this new paused job.
	if err := checkScheduleUnchanged(path, nil); err == nil {
		t.Fatal("stale setup accepted")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("stale form changed the paused job")
	}
	saved, err := readSchedule(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkScheduleUnchanged(path, saved); err != nil {
		t.Fatal(err)
	}
	s.RetentionDays++
	writeTestSchedule(t, path, s)
	if err := checkScheduleUnchanged(path, saved); err == nil {
		t.Fatal("concurrent edit accepted")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := checkScheduleUnchanged(path, saved); err == nil {
		t.Fatal("concurrent removal accepted")
	}
	if err := os.WriteFile(path, []byte("unmanaged cron"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := checkScheduleUnchanged(path, nil); err == nil {
		t.Fatal("schedule read error ignored")
	}
}

func TestServerTimezoneSources(t *testing.T) {
	zoneinfo := "/usr/share/zoneinfo"
	for _, tc := range []struct {
		name, named, local, want string
		badName, badLocal        bool
	}{
		{name: "missing-both", want: "UTC"},
		{name: "local-only", local: "Europe/Berlin", want: "server local"},
		{name: "matching", named: "Europe/Berlin", local: "Europe/Berlin", want: "Europe/Berlin"},
		{name: "aliases", named: "US/Central", local: "America/Chicago", want: "US/Central"},
		{name: "missing-local-utc", named: "Etc/UTC", want: "UTC"},
		{name: "missing-local-conflict", named: "Europe/Berlin"},
		{name: "conflicting-files", named: "Etc/UTC", local: "Europe/Berlin"},
		{name: "same-offset-now-different-dst", named: "Etc/UTC", local: "Europe/London"},
		{name: "unreadable-name", badName: true, local: "Etc/UTC"},
		{name: "unreadable-local", named: "Etc/UTC", badLocal: true},
		{name: "invalid-zone", named: "not-a-timezone", local: "Etc/UTC"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			namePath, localPath := filepath.Join(root, "timezone"), filepath.Join(root, "localtime")
			if tc.named != "" {
				if err := os.WriteFile(namePath, []byte(tc.named+"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if tc.local != "" {
				data, err := os.ReadFile(filepath.Join(zoneinfo, tc.local))
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(localPath, data, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			// A directory provides a reliable non-ENOENT read failure even as root.
			if tc.badName {
				if err := os.Mkdir(namePath, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if tc.badLocal {
				if err := os.Mkdir(localPath, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("TZ", "Pacific/Honolulu")
			loc, err := readServerLocation(namePath, localPath, zoneinfo)
			if tc.want == "" {
				if err == nil {
					t.Fatal("ambiguous or unreadable timezone accepted", loc)
				}
				return
			}
			if err != nil || loc.String() != tc.want {
				t.Fatalf("%v %v, want %s", loc, err, tc.want)
			}
		})
	}
}

func TestTimezoneWarningKeepsStatusControlsAvailable(t *testing.T) {
	root := t.TempDir()
	path, history := filepath.Join(root, "cron"), filepath.Join(root, "history")
	now := time.Now()
	s := newSchedule(testConfig(t), "/bin/nomctl", "host", now.Add(-48*time.Hour))
	writeTestSchedule(t, path, s)
	timezoneErr := errors.New("timezone sources disagree")
	status, err := scheduleStatusWithTimezone(path, history, now, timezoneErr)
	if err != nil || status.Schedule == nil || status.State != "enabled" || status.TimezoneError == "" || status.NextRun != nil || status.MissedRun != nil {
		t.Fatalf("%+v %v", status, err)
	}
	var output bytes.Buffer
	WriteScheduleStatus(&output, status)
	if !strings.Contains(output.String(), "Next scheduled slot: unknown") || !strings.Contains(output.String(), timezoneErr.Error()) {
		t.Fatal(output.String())
	}
}
