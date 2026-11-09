package nodeupgrade

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/0x3639/nomctl/internal/backup"
	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/execx"
)

func stub(t *testing.T, dir, name, script string) {
	t.Helper()
	path := filepath.Join(dir, name)
	put(t, path, "#!/bin/sh\nset -eu\n"+script)
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func fixture(t *testing.T) (config.Config, string, string) {
	t.Helper()
	root := t.TempDir()
	cfg := config.Default()
	cfg.ZnnDir = filepath.Join(root, "data")
	cfg.BackupDir = filepath.Join(root, "backup")
	cfg.InstallDir = filepath.Join(root, "bin")
	cfg.MinFreeSpaceKB = 0
	cfg.BackupCadenceDays = 365
	// These integration tests exercise the real backup filesystem. Skip when
	// its capacity cannot support even this tiny fixture; low-space cases set
	// their constrained reserve separately after setup.
	if err := backup.CheckRestoreSpace(cfg, root, 1<<20); err != nil {
		t.Skipf("integration test needs backup capacity: %v", err)
	}
	put(t, cfg.BinaryPath(), "old")
	put(t, filepath.Join(cfg.ZnnDir, "nom", "state"), "before-upgrade")
	put(t, filepath.Join(cfg.ZnnDir, "wallet", "secret"), "wallet-secret")
	put(t, filepath.Join(cfg.ZnnDir, "config.json"), "config-secret")
	binary := filepath.Join(root, "verified")
	put(t, binary, "new")
	state := filepath.Join(root, "state")
	put(t, state, "active")
	events := filepath.Join(root, "events")
	t.Setenv("TEST_STATE", state)
	t.Setenv("TEST_EVENTS", events)
	t.Setenv("TEST_BINARY", cfg.BinaryPath())
	t.Setenv("TEST_DATA", cfg.ZnnDir)
	for _, v := range []string{"FAIL_STOP", "FAIL_COPY", "FAIL_START", "FAIL_ARCHIVE", "INACTIVE_AFTER_START", "AFTER_STOP"} {
		t.Setenv(v, "")
	}
	commands := filepath.Join(root, "commands")
	stub(t, commands, "systemctl", `
case "$1" in
 status) exit 0 ;;
 show) printf 'LoadState=loaded\nActiveState=%s\n' "$(cat "$TEST_STATE")" ;;
 is-active) [ "$(cat "$TEST_STATE")" = active ] && exit 0; exit 3 ;;
 stop)
  echo stop >> "$TEST_EVENTS"
  [ -z "$FAIL_STOP" ] || exit 1
  echo "${AFTER_STOP:-inactive}" > "$TEST_STATE" ;;
 start)
  echo "start:$(cat "$TEST_BINARY")" >> "$TEST_EVENTS"
  [ -z "$FAIL_START" ] || exit 1
  echo active > "$TEST_STATE"
  [ -z "$INACTIVE_AFTER_START" ] || echo inactive > "$TEST_STATE"
  echo after-upgrade > "$TEST_DATA/nom/state" ;;
 *) exit 1 ;;
esac
`)
	stub(t, commands, "cp", `
[ "$(cat "$TEST_STATE")" = inactive ] || exit 8
echo copy >> "$TEST_EVENTS"
[ -z "$FAIL_COPY" ] || exit 1
exec /bin/cp "$@"
`)
	stub(t, commands, "tar", `
echo "archive:$(cat "$TEST_BINARY")" >> "$TEST_EVENTS"
[ -z "$FAIL_ARCHIVE" ] || exit 1
exec /usr/bin/tar "$@"
`)
	t.Setenv("PATH", commands+string(os.PathListSeparator)+os.Getenv("PATH"))
	execx.Configure(false, nil)
	return cfg, binary, events
}

func TestUpgradeSequenceAndProtectedRestorePoint(t *testing.T) {
	cfg, binary, events := fixture(t)
	// A recent backup must not suppress the mandatory upgrade backup.
	oldArchive := filepath.Join(cfg.BackupDir, "go-zenon_backup_old.tar.gz")
	put(t, oldArchive, "old archive")
	put(t, backup.HashPath(oldArchive), "old hash")
	cfg.MaxBackups = 1
	if err := installPrepared(cfg, binary, "v1"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(events)
	if string(data) != "stop\ncopy\nstart:new\narchive:new\n" {
		t.Fatalf("wrong operation order: %s", data)
	}
	old, _ := os.ReadFile(cfg.BinaryPath() + ".previous")
	if string(old) != "old" {
		t.Fatal("previous binary missing")
	}
	info, err := os.Stat(cfg.BinaryPath())
	if err != nil || info.Mode().Perm()&0o111 == 0 {
		t.Fatal("installed binary not executable", err)
	}
	archives, err := backup.List(cfg)
	if err != nil || len(archives) != 1 || archives[0].Path != oldArchive {
		t.Fatalf("ordinary backup changed: %+v %v", archives, err)
	}
	points, err := backup.ListRestorePoints(cfg)
	if err != nil || len(points) != 1 || !points[0].Complete {
		t.Fatalf("restore point: %+v %v", points, err)
	}
	archive := points[0].ArchivePath()
	savedBinary, err := os.ReadFile(points[0].BinaryPath())
	if err != nil || string(savedBinary) != "old" {
		t.Fatal("matching binary not preserved", err)
	}
	sum, err := backup.SHA256File(archive)
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := os.ReadFile(backup.HashPath(archive))
	if strings.TrimSpace(string(stored)) != sum {
		t.Fatal("bad sidecar")
	}
	f, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	found := false
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(h.Name, "wallet") || strings.Contains(h.Name, "config.json") {
			t.Fatal("wallet/config entered chain backup")
		}
		if strings.TrimPrefix(h.Name, "./") == "nom/state" {
			data, err := io.ReadAll(tr)
			if err != nil || string(data) != "before-upgrade" {
				t.Fatal("archive captured post-start data", err)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("missing pre-upgrade state")
	}
	snapshots, _ := filepath.Glob(filepath.Join(backup.RestorePointsDir(cfg), "*", "data"))
	if len(snapshots) != 0 {
		t.Fatal("finished snapshot not cleaned")
	}
}

func TestUpgradeRequiresConfirmedStop(t *testing.T) {
	cfg, binary, events := fixture(t)
	t.Setenv("AFTER_STOP", "active")
	if err := installPrepared(cfg, binary, "test"); err == nil || !strings.Contains(err.Error(), "did not stop") {
		t.Fatalf("expected stop verification failure, got %v", err)
	}
	data, _ := os.ReadFile(events)
	if string(data) != "stop\n" {
		t.Fatalf("data operation ran without a confirmed stop: %s", data)
	}
	installed, _ := os.ReadFile(cfg.BinaryPath())
	if string(installed) != "old" {
		t.Fatal("binary replaced without a confirmed stop")
	}
}

func TestUpgradeRecoversPreviouslyStartingNode(t *testing.T) {
	cfg, binary, events := fixture(t)
	put(t, os.Getenv("TEST_STATE"), "activating")
	t.Setenv("FAIL_COPY", "1")
	if err := installPrepared(cfg, binary, "test"); err == nil {
		t.Fatal("copy failure was ignored")
	}
	data, _ := os.ReadFile(events)
	if string(data) != "stop\ncopy\nstart:old\n" {
		t.Fatalf("previously starting node not recovered: %s", data)
	}
}

// Run signal scenarios in a separate process so failures cannot terminate the
// main test runner and each scenario starts with the default signal handlers.
func TestUpgradeSignals(t *testing.T) {
	for _, sig := range []string{"INT", "TERM"} {
		for _, phase := range []string{"complete", "stop", "copy"} {
			t.Run(sig+"/"+phase, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestUpgradeSignalHelper$", "-test.v")
				cmd.Env = append(os.Environ(), "NOMCTL_UPGRADE_SIGNAL_TEST="+sig, "NOMCTL_UPGRADE_SIGNAL_PHASE="+phase)
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("signal recovery: %v\n%s", err, out)
				}
			})
		}
	}
}

func TestUpgradeSignalHelper(t *testing.T) {
	sig := os.Getenv("NOMCTL_UPGRADE_SIGNAL_TEST")
	if sig == "" {
		t.Skip("subprocess helper")
	}
	cfg, binary, events := fixture(t)
	commands := filepath.Dir(events) + "/commands"
	phase := os.Getenv("NOMCTL_UPGRADE_SIGNAL_PHASE")
	if phase == "stop" {
		path := filepath.Join(commands, "systemctl")
		script, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		put(t, path, strings.Replace(string(script), "stop)\n", "stop)\n  echo inactive > \"$TEST_STATE\"\n  kill -"+sig+" \"$PPID\"\n  kill -"+sig+" $$\n", 1))
	} else {
		script := "kill -" + sig + " \"$PPID\"\n"
		if phase == "copy" {
			script += "kill -" + sig + " $$\n"
		} else {
			script += "exec /bin/cp \"$@\"\n"
		}
		stub(t, commands, "cp", script)
	}
	err := installPrepared(cfg, binary, "test")
	if (err == nil) != (phase == "complete") {
		t.Fatalf("unexpected transaction result: %v", err)
	}
	state, _ := os.ReadFile(os.Getenv("TEST_STATE"))
	if strings.TrimSpace(string(state)) != "active" {
		t.Fatalf("node left stopped after signal: %s", state)
	}
	want := "old"
	if phase == "complete" {
		want = "new"
	}
	installed, _ := os.ReadFile(cfg.BinaryPath())
	if string(installed) != want {
		t.Fatalf("installed %q, want %q", installed, want)
	}
	snapshots, _ := filepath.Glob(filepath.Join(backup.RestorePointsDir(cfg), "*", "data"))
	if len(snapshots) != 0 {
		t.Fatal("incomplete snapshot retained after signal recovery")
	}
}

func TestUpgradeFailures(t *testing.T) {
	for _, tc := range []struct {
		name, fail, wantEvents, wantBinary string
		archive, snapshot                  bool
	}{
		{"stop", "FAIL_STOP", "stop\n", "old", false, false},
		{"copy", "FAIL_COPY", "stop\ncopy\nstart:old\n", "old", false, false},
		{"install", "install", "stop\ncopy\nstart:old\narchive:old\n", "old", true, false},
		{"start", "FAIL_START", "stop\ncopy\nstart:new\narchive:new\n", "new", true, false},
		{"inactive", "INACTIVE_AFTER_START", "stop\ncopy\nstart:new\narchive:new\n", "new", true, false},
		{"archive", "FAIL_ARCHIVE", "stop\ncopy\nstart:new\narchive:new\n", "new", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, binary, events := fixture(t)
			if tc.fail == "install" {
				if err := os.Mkdir(cfg.BinaryPath()+".previous", 0o700); err != nil {
					t.Fatal(err)
				}
			} else {
				t.Setenv(tc.fail, "yes")
			}
			err := installPrepared(cfg, binary, "v1")
			if err == nil {
				t.Fatal("failure not reported")
			}
			if tc.fail == "FAIL_ARCHIVE" && !strings.Contains(err.Error(), "is running, but backup finalization failed") {
				t.Fatal(err)
			}
			data, _ := os.ReadFile(events)
			if string(data) != tc.wantEvents {
				t.Fatalf("events: %s; error: %v", data, err)
			}
			installed, _ := os.ReadFile(cfg.BinaryPath())
			if string(installed) != tc.wantBinary {
				t.Fatalf("binary: %s", installed)
			}
			points, err := backup.ListRestorePoints(cfg)
			if err != nil {
				t.Fatal(err)
			}
			complete := 0
			for _, p := range points {
				if p.Complete {
					complete++
				}
			}
			if (complete > 0) != tc.archive {
				t.Fatalf("restore points: %+v", points)
			}
			snapshots, _ := filepath.Glob(filepath.Join(backup.RestorePointsDir(cfg), "*", "data"))
			if (len(snapshots) > 0) != tc.snapshot {
				t.Fatalf("snapshots: %v", snapshots)
			}
			if tc.snapshot {
				// A later backup must not destroy the retained pre-upgrade data.
				next, err := backup.Prepare(cfg)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = next.Discard() }()
				old, err := os.ReadFile(filepath.Join(snapshots[0], "nom", "state"))
				if err != nil || string(old) != "before-upgrade" {
					t.Fatal("recovery snapshot lost", err)
				}
			}
		})
	}
}

func TestPreflightFailureDoesNotStopNode(t *testing.T) {
	cfg, binary, events := fixture(t)
	cfg.MinFreeSpaceKB = 1 << 62
	if err := installPrepared(cfg, binary, "v1"); err == nil {
		t.Fatal("space failure ignored")
	}
	if _, err := os.Stat(events); !os.IsNotExist(err) {
		t.Fatal("service touched before backup preflight")
	}
}
