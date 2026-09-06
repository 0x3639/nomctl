package support

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/execx"
)

// stubTools puts fake systemctl/journalctl/etc on PATH that echo their args.
func stubTools(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	for _, tool := range []string{"systemctl", "journalctl", "free", "df", "ps", "coredumpctl", "uname", "uptime"} {
		script := "#!/bin/sh\necho \"" + tool + " $*\"\n"
		if tool == "systemctl" {
			script = "#!/bin/sh\ncase \"$1\" in show) echo 'ActiveState=active'; echo 'SubState=running'; echo 'MainPID=0'; echo 'ControlGroup=/system.slice/x';; *) echo \"systemctl $*\";; esac\n"
		}
		if err := os.WriteFile(filepath.Join(dir, tool), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	execx.Configure(false, nil)
}

func TestCollectWritesEveryFile(t *testing.T) {
	stubTools(t)
	cfg := config.Default()
	cfg.ZnnDir = t.TempDir()
	cfg.LogFile = filepath.Join(t.TempDir(), "nomctl.log")
	if err := os.WriteFile(cfg.LogFile, []byte("line1\npassword=abc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(cfg.ZnnDir, "log"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.ZnnDir, "log", "znnd.log"), []byte("INFO ok\nFATAL panic: boom\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.ZnnDir, "config.json"), []byte(`{"secret":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "bundle")
	res, err := Collect(context.Background(), cfg, Options{OutputDir: out, Since: "1 hour ago", Version: "test", NodeURL: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"02-summary.txt", "03-service-status.txt", "04-service-properties.txt", "05-service-unit.txt", "06-service-journal.log", "07-kernel-journal.log", "08-system-warnings.log", "09-live-process.txt", "09-cgroup.txt", "10-host-resources.txt", "11-app-log-inventory.txt", "13-crash-markers.log", "14-coredumps-list.txt", "15-coredumps-info.txt", "16-binary.txt", "17-oom-and-boots.txt", "18-node-rpc.json", "19-nomctl.txt", "20-status.txt"} {
		if _, err := os.Stat(filepath.Join(out, name)); err != nil {
			t.Errorf("missing %s", name)
		}
	}
	if _, err := os.Stat(filepath.Join(out, "12-app-log-tails", "01-znnd.log.tail")); err != nil {
		t.Error("missing log tail")
	}
	markers, _ := os.ReadFile(res.Markers)
	if !strings.Contains(string(markers), "panic: boom") {
		t.Errorf("crash markers should include the app log line: %s", markers)
	}
	if st, _ := os.Stat(out); st.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %o", st.Mode().Perm())
	}
	st, err := os.Stat(res.Archive)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("archive: %v mode %v", err, st)
	}
	names := tarNames(t, res.Archive)
	if !names["bundle/02-summary.txt"] || !names["bundle/12-app-log-tails/01-znnd.log.tail"] {
		t.Errorf("archive contents wrong: %v", names)
	}
	for n := range names {
		if strings.Contains(n, "config.json") {
			t.Error("config.json must never be collected")
		}
	}
	rpc, _ := os.ReadFile(filepath.Join(out, "18-node-rpc.json"))
	if !strings.Contains(string(rpc), `"error"`) {
		t.Errorf("unreachable node must be recorded as error: %s", rpc)
	}
	nomctl, _ := os.ReadFile(filepath.Join(out, "19-nomctl.txt"))
	if strings.Contains(string(nomctl), "password=abc") || !strings.Contains(string(nomctl), "version=test") {
		t.Errorf("nomctl file: %s", nomctl)
	}
	summary, _ := os.ReadFile(filepath.Join(out, "02-summary.txt"))
	if !strings.Contains(string(summary), "service=go-zenon.service") {
		t.Errorf("summary: %s", summary)
	}
}

func tarNames(t *testing.T, archive string) map[string]bool {
	t.Helper()
	f, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	names := map[string]bool{}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names[h.Name] = true
	}
	return names
}

func TestDefaultOutputDir(t *testing.T) {
	d := DefaultOutputDir(time.Date(2026, 9, 6, 1, 2, 3, 0, time.UTC))
	if !strings.HasPrefix(d, "/root/nomctl-support-") || !strings.HasSuffix(d, "-20260906T010203Z") {
		t.Errorf("dir = %q", d)
	}
}
