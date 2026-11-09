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
	for _, mode := range []string{
		"success", "existing-crond", "install-cron", "install-crond", "missing",
		"query-failure", "update-failure", "install-failure", "reload-failure", "still-missing",
		"enable-failure", "inactive", "state-query-failure", "disable-failure", "rename-failure",
	} {
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
			t.Setenv("CRON_TEST_INSTALLED", filepath.Join(dest, "installed"))
			stubCommand(t, commands, "systemctl", `
echo "systemctl $*" >> "$CRON_TEST_EVENTS"
case "$1" in
 status)
   [ "$CRON_TEST_MODE" != query-failure ] || exit 1
   [ "$2" != nomctl-backup.timer ] || exit 3
   case "$CRON_TEST_MODE" in
     missing|still-missing) exit 4 ;;
     install-cron|install-crond|update-failure|install-failure|reload-failure)
       [ -f "$CRON_TEST_INSTALLED" ] || exit 4 ;;
   esac
   case "$CRON_TEST_MODE" in
     existing-crond|install-crond) [ "$2" = crond.service ] || exit 4 ;;
     *) [ "$2" = cron.service ] || exit 4 ;;
   esac
   exit 3 ;;
 show)
   [ "$CRON_TEST_MODE" != state-query-failure ] || exit 1
   echo LoadState=loaded
   if [ "$CRON_TEST_MODE" = inactive ]; then
     echo ActiveState=inactive
   else
     echo ActiveState=active
   fi ;;
 daemon-reload) [ "$CRON_TEST_MODE" != reload-failure ] || exit 1 ;;
 disable) [ "$CRON_TEST_MODE" != disable-failure ] || exit 1 ;;
 enable) [ "$CRON_TEST_MODE" != enable-failure ] || exit 1 ;;
 is-enabled) ;;
 *) exit 1 ;;
esac
`)
			if mode != "missing" {
				stubCommand(t, commands, "apt-get", `
echo "apt-get $*" >> "$CRON_TEST_EVENTS"
[ "$DEBIAN_FRONTEND" = noninteractive ] || exit 1
case "$*" in
 "update -qq") [ "$CRON_TEST_MODE" != update-failure ] || exit 1 ;;
 "install -y --no-install-recommends cron")
   [ "$NEEDRESTART_MODE" = l ] || exit 1
   [ "$CRON_TEST_MODE" != install-failure ] || exit 1
   : > "$CRON_TEST_INSTALLED" ;;
 *) exit 1 ;;
esac
`)
			}
			// Isolate every external command: never fall through to the host's
			// apt or systemctl, even when testing missing dependencies.
			t.Setenv("PATH", commands)
			success := mode == "success" || mode == "existing-crond" || mode == "install-cron" || mode == "install-crond"
			err := installLocalSchedule(cfg, "/usr/local/bin/nomctl", "test-host", path)
			if (err == nil) != success {
				t.Fatalf("unexpected result: %v", err)
			}
			if success {
				// Reconfiguration replaces the same file, never reinstalls cron,
				// and never runs a backup or touches the node service.
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
				data, readErr := os.ReadFile(path)
				if readErr != nil || string(data) != "previous job\n" {
					t.Fatal("failed setup changed existing job", readErr)
				}
			}
			data, readErr := os.ReadFile(events)
			if readErr != nil {
				t.Fatal(readErr)
			}
			calls := string(data)
			if strings.Contains(calls, cfg.ServiceName) {
				t.Fatal("schedule setup touched node")
			}
			if success {
				unit := "cron.service"
				if mode == "existing-crond" || mode == "install-crond" {
					unit = "crond.service"
				}
				for _, call := range []string{"enable --now " + unit, "show --property=LoadState --property=ActiveState " + unit, "disable --now nomctl-backup.timer"} {
					if !strings.Contains(calls, call) {
						t.Fatalf("missing %q in:\n%s", call, calls)
					}
				}
				installs := 0
				if strings.HasPrefix(mode, "install-") {
					installs = 1
				}
				if strings.Count(calls, "apt-get install") != installs || strings.Count(calls, "apt-get update") != installs {
					t.Fatalf("unnecessary or missing install:\n%s", calls)
				}
			} else {
				want := map[string]string{
					"missing":             "automatic installation requires Debian/Ubuntu",
					"query-failure":       "cannot query unit",
					"update-failure":      "refresh package lists",
					"install-failure":     "install cron for scheduled backups",
					"reload-failure":      "reload systemd",
					"still-missing":       "neither cron.service nor crond.service was found",
					"enable-failure":      "enable backup scheduler",
					"inactive":            "did not start",
					"state-query-failure": "cannot determine state",
				}[mode]
				if want != "" && !strings.Contains(err.Error(), want) {
					t.Fatalf("error %v does not contain %q", err, want)
				}
				if mode != "disable-failure" && mode != "rename-failure" && strings.Contains(calls, "disable --now") {
					t.Fatalf("old timer disabled before cron was ready:\n%s", calls)
				}
				if mode == "query-failure" && strings.Contains(calls, "apt-get") {
					t.Fatal("systemd query failure triggered installation")
				}
				if mode == "update-failure" && strings.Contains(calls, "apt-get install") {
					t.Fatal("package installation proceeded after failed update")
				}
			}
			if mode == "rename-failure" && !strings.Contains(calls, "enable --now nomctl-backup.timer") {
				t.Fatal("old timer not recovered")
			}
			staged, _ := filepath.Glob(filepath.Join(dest, ".nomctl-backup-*"))
			if len(staged) > 0 {
				t.Fatal("staged cron files leaked")
			}
		})
	}
}
