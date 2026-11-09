package backup

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCronDailySettingsAndQuoting(t *testing.T) {
	cfg := testConfig(t)
	cfg.BackupRetentionDays, cfg.BackupCadenceDays, cfg.BackupHour = 30, 7, 3
	cfg.BackupDir = `/data/it's 100% $(false) "backups"`
	executable := filepath.Join(t.TempDir(), "nomctl's 100% test")
	script := "#!/bin/sh\nprintf '%s\\n' \"$NOMCTL_BACKUP_DIR\" \"$NOMCTL_ZNN_DIR\" \"$NOMCTL_SERVICE_NAME\" \"$NOMCTL_LOG_FILE\" \"$NOMCTL_MIN_FREE_SPACE_KB\" \"$@\"\n"
	if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	entry, err := CronEntry(cfg, executable, "node1")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(entry), "\n")
	job := lines[len(lines)-1]
	if !strings.Contains(job, " 3 * * * root ") || !strings.Contains(job, `100\%`) {
		t.Fatal(entry)
	}
	_, command, ok := strings.Cut(job, " root ")
	if !ok {
		t.Fatal("missing root cron job")
	}
	// Cron removes escapes from percent signs before passing to /bin/sh.
	out, err := exec.Command("/bin/sh", "-c", strings.ReplaceAll(command, `\%`, "%")).CombinedOutput()
	if err != nil {
		t.Fatalf("cron command failed: %s %v", out, err)
	}
	want := strings.Join([]string{cfg.BackupDir, cfg.ZnnDir, cfg.ServiceName, cfg.LogFile, "0", "backup", "--skip-preflight", "--cadence", "0", "--max-backups", "30", "--retention-days", "30", ""}, "\n")
	if string(out) != want {
		t.Fatalf("cron lost selected settings:\n%s\nwant:\n%s", out, want)
	}
	for _, value := range []string{"bad\njob", "bad\rjob", "bad\x00job"} {
		cfg.BackupDir = value
		if _, err := CronEntry(cfg, executable, "node1"); err == nil {
			t.Fatal("invalid cron value accepted")
		}
	}
	for _, days := range []int{0, 31} {
		cfg = testConfig(t)
		cfg.BackupRetentionDays = days
		if _, err := CronEntry(cfg, executable, "node1"); err == nil {
			t.Fatal("invalid retention accepted")
		}
	}
}

func stubCommand(t *testing.T, dir, name, script string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nset -eu\n"+script), 0o700); err != nil {
		t.Fatal(err)
	}
}

func TestInstallCronMigrationAndFailures(t *testing.T) {
	for _, mode := range []string{"success", "missing", "disable-failure", "rename-failure"} {
		t.Run(mode, func(t *testing.T) {
			cfg := testConfig(t)
			commands, dest := t.TempDir(), t.TempDir()
			path := filepath.Join(dest, "nomctl-backup")
			if mode == "rename-failure" {
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(path, []byte("previous job\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			events := filepath.Join(dest, "events")
			t.Setenv("CRON_TEST_EVENTS", events)
			t.Setenv("CRON_TEST_MODE", mode)
			stubCommand(t, commands, "systemctl", `
echo "$*" >> "$CRON_TEST_EVENTS"
case "$1" in
 status) [ "$CRON_TEST_MODE" != missing ] || exit 4 ;;
 disable) [ "$CRON_TEST_MODE" != disable-failure ] || exit 1 ;;
 enable|is-enabled) ;;
 *) exit 1 ;;
esac
`)
			t.Setenv("PATH", commands+":"+os.Getenv("PATH"))
			err := installLocalSchedule(cfg, "/usr/local/bin/nomctl", "test-host", path)
			if (err == nil) != (mode == "success") {
				t.Fatalf("unexpected result: %v", err)
			}
			if mode == "success" {
				// Reconfiguration replaces the same file and never runs a backup.
				cfg.BackupRetentionDays = 30
				if err := installLocalSchedule(cfg, "/usr/local/bin/nomctl", "test-host", path); err != nil {
					t.Fatal(err)
				}
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Count(string(data), " * * * root ") != 1 || !strings.Contains(string(data), "retention 30 days") {
					t.Fatal(string(data))
				}
				info, err := os.Stat(path)
				if err != nil || info.Mode().Perm() != 0o644 {
					t.Fatalf("cron permissions: %v %v", info, err)
				}
			} else if mode != "rename-failure" {
				data, err := os.ReadFile(path)
				if err != nil || string(data) != "previous job\n" {
					t.Fatal("failed setup changed existing job", err)
				}
			}
			data, _ := os.ReadFile(events)
			if strings.Contains(string(data), "go-zenon") {
				t.Fatal("schedule setup touched node")
			}
			if mode == "success" && !strings.Contains(string(data), "disable --now nomctl-backup.timer") {
				t.Fatal("old timer not disabled")
			}
			if mode == "rename-failure" && !strings.Contains(string(data), "enable --now nomctl-backup.timer") {
				t.Fatal("old timer not recovered")
			}
			staged, _ := filepath.Glob(filepath.Join(dest, ".nomctl-backup-*"))
			if len(staged) > 0 {
				t.Fatal("staged cron files leaked")
			}
		})
	}
}
