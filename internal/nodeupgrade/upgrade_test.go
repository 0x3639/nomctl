package nodeupgrade

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	for _, v := range []string{"FAIL_STOP", "FAIL_COPY", "FAIL_START", "FAIL_ARCHIVE", "INACTIVE_AFTER_START"} {
		t.Setenv(v, "")
	}
	commands := filepath.Join(root, "commands")
	stub(t, commands, "systemctl", `
case "$1" in
 status) exit 0 ;;
 is-active) [ "$(cat "$TEST_STATE")" = active ] && exit 0; exit 3 ;;
 stop)
  echo stop >> "$TEST_EVENTS"
  [ -z "$FAIL_STOP" ] || exit 1
  echo inactive > "$TEST_STATE" ;;
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

func TestUpgradeSequenceAndPreUpgradeBackup(t *testing.T) {
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
	if err != nil || len(archives) != 1 || archives[0].Path == oldArchive {
		t.Fatalf("backup/retention: %+v %v", archives, err)
	}
	sum, err := backup.SHA256File(archives[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := os.ReadFile(backup.HashPath(archives[0].Path))
	if strings.TrimSpace(string(stored)) != sum {
		t.Fatal("bad sidecar")
	}
	f, err := os.Open(archives[0].Path)
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
	snapshots, _ := filepath.Glob(filepath.Join(cfg.BackupDir, "snapshot-*"))
	if len(snapshots) != 0 {
		t.Fatal("finished snapshot not cleaned")
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
			archives, err := backup.List(cfg)
			if err != nil || (len(archives) > 0) != tc.archive {
				t.Fatalf("archives: %+v %v", archives, err)
			}
			snapshots, _ := filepath.Glob(filepath.Join(cfg.BackupDir, "snapshot-*"))
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
