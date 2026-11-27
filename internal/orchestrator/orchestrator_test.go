package orchestrator

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/0x3639/nomctl/internal/config"
)

type fakeHost struct {
	calls  []string
	exists bool
	slept  time.Duration
}

func (f *fakeHost) install(t *testing.T) {
	t.Helper()
	oldStop, oldStart, oldExists, oldSleep := stopService, startService, unitExists, sleep
	stopService = func(n string) error { f.calls = append(f.calls, "stop "+n); return nil }
	startService = func(n string) error { f.calls = append(f.calls, "start "+n); return nil }
	unitExists = func(string) (bool, error) { return f.exists, nil }
	sleep = func(d time.Duration) { f.slept += d }
	t.Cleanup(func() { stopService, startService, unitExists, sleep = oldStop, oldStart, oldExists, oldSleep })
}

func testConfig(t *testing.T) config.Config {
	t.Helper()
	dir := t.TempDir()
	for _, d := range []string{"queues/sub", "events", "config"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "config", "config.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "queues", "sub", "q"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return config.Config{OrchestratorService: "orchestrator", OrchestratorDir: dir}
}

func TestHardReset(t *testing.T) {
	cfg := testConfig(t)
	h := &fakeHost{exists: true}
	h.install(t)
	if err := HardReset(cfg); err != nil {
		t.Fatal(err)
	}
	if strings.Join(h.calls, ",") != "stop orchestrator,start orchestrator" {
		t.Errorf("calls = %v", h.calls)
	}
	if h.slept != SettleDelay {
		t.Errorf("slept %s, want %s", h.slept, SettleDelay)
	}
	for _, d := range ResetDirs {
		if _, err := os.Stat(filepath.Join(cfg.OrchestratorDir, d)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s not deleted: %v", d, err)
		}
	}
	if _, err := os.Stat(filepath.Join(cfg.OrchestratorDir, "config", "config.json")); err != nil {
		t.Error("config must be untouched")
	}
}

func TestHardResetSkipsMissingDirs(t *testing.T) {
	cfg := config.Config{OrchestratorService: "orchestrator", OrchestratorDir: t.TempDir()}
	h := &fakeHost{exists: true}
	h.install(t)
	if err := HardReset(cfg); err != nil {
		t.Fatal(err)
	}
	if len(h.calls) != 2 {
		t.Errorf("calls = %v", h.calls)
	}
}

func TestHardResetRefusesUnsafeDir(t *testing.T) {
	h := &fakeHost{exists: true}
	h.install(t)
	for _, dir := range []string{"", ".", "queues", "/", "//", "/../", "relative/path"} {
		err := HardReset(config.Config{OrchestratorService: "orchestrator", OrchestratorDir: dir})
		if !errors.Is(err, ErrBadDir) {
			t.Errorf("dir %q: err = %v", dir, err)
		}
	}
	if len(h.calls) != 0 {
		t.Errorf("service touched: %v", h.calls)
	}
	if got, err := ValidateDir("/root/.orchestrator/"); err != nil || got != "/root/.orchestrator" {
		t.Errorf("ValidateDir = %q, %v", got, err)
	}
}

func TestHardResetRefusesWithoutUnit(t *testing.T) {
	cfg := testConfig(t)
	h := &fakeHost{exists: false}
	h.install(t)
	if err := HardReset(cfg); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("err = %v", err)
	}
	if len(h.calls) != 0 {
		t.Errorf("service touched: %v", h.calls)
	}
	if _, err := os.Stat(filepath.Join(cfg.OrchestratorDir, "queues", "sub", "q")); err != nil {
		t.Error("data touched")
	}
}

func TestEnsureDataDirPreservesExistingPermissions(t *testing.T) {
	parent := t.TempDir()
	existing := filepath.Join(parent, "existing")
	if err := os.Mkdir(existing, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(existing, 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureDataDir(existing); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(existing); err != nil || st.Mode().Perm() != 0o750 {
		t.Fatalf("existing dir mode changed to %v (err=%v), want 0750", st.Mode().Perm(), err)
	}

	created := filepath.Join(parent, "fresh")
	if _, err := EnsureDataDir(created); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(created); err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("new dir mode = %v (err=%v), want 0700", st.Mode().Perm(), err)
	}
}

func TestUnitFileSetsHomeForOrchestratorDir(t *testing.T) {
	cfg := config.Default()
	cfg.OrchestratorDir = "/srv/bridge/.orchestrator"
	unit, err := UnitFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(unit, `Environment="HOME=/srv/bridge"`) {
		t.Fatalf("unit missing HOME environment:\n%s", unit)
	}

	cfg.OrchestratorDir = "/srv/bridge/custom-dir"
	if _, err := UnitFile(cfg); err == nil {
		t.Fatal("expected UnitFile to reject OrchestratorDir not ending in /.orchestrator")
	}
}

func TestEnsureServicePreservesVendorUnitAndRollsBackOnEnableFailure(t *testing.T) {
	tmp := t.TempDir()
	unitPath := filepath.Join(tmp, "etc", "orchestrator.service")
	cfg := config.Default()
	cfg.OrchestratorDir = filepath.Join(tmp, ".orchestrator")

	oldExists, oldLoaded, oldWrite, oldReload, oldEnable, oldUnitPath := unitExists, unitLoaded, writeUnit, daemonReload, enableUnit, unitPathFor
	t.Cleanup(func() {
		unitExists, unitLoaded, writeUnit, daemonReload, enableUnit, unitPathFor = oldExists, oldLoaded, oldWrite, oldReload, oldEnable, oldUnitPath
	})
	unitPathFor = func(config.Config) string { return unitPath }

	// 1. Existing vendor unit (/usr/lib/systemd/system) reported by LoadState must not be shadowed.
	wrote := false
	unitLoaded = func(string) (bool, error) { return true, nil }
	writeUnit = func(string, string) error { wrote = true; return nil }
	created, _, err := EnsureService(cfg)
	if err != nil || created || wrote {
		t.Fatalf("vendor unit should be preserved: created=%v wrote=%v err=%v", created, wrote, err)
	}

	// 2. If writeUnit fails partway and leaves a partial file, it is cleaned up so retries succeed.
	unitLoaded = func(string) (bool, error) { return false, nil }
	writeUnit = func(path, _ string) error {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		_ = os.WriteFile(path, []byte("partial"), 0o644)
		return errors.New("disk full")
	}
	if created, _, err := EnsureService(cfg); err == nil || created {
		t.Fatalf("expected partial writeUnit to fail, got created=%v err=%v", created, err)
	}
	if _, err := os.Stat(unitPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed writeUnit left partial unit file behind: %v", err)
	}

	// 3. If enableUnit fails on fresh install, the newly written unit file is removed so retry works.
	writeUnit = func(path, content string) error {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		return os.WriteFile(path, []byte(content), 0o644)
	}
	daemonReload = func() error { return nil }
	enableCalls := 0
	enableUnit = func(string) error {
		enableCalls++
		if enableCalls == 1 {
			return errors.New("enable failed")
		}
		return nil
	}
	if created, _, err := EnsureService(cfg); err == nil || created {
		t.Fatalf("expected first EnsureService to fail, got created=%v err=%v", created, err)
	}
	if _, err := os.Stat(unitPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed EnsureService left unit file behind: %v", err)
	}
	if created, _, err := EnsureService(cfg); err != nil || !created || enableCalls != 2 {
		t.Fatalf("retry EnsureService failed: created=%v enableCalls=%d err=%v", created, enableCalls, err)
	}
}

func TestParseSystemdExecStartPath(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{`{ path=/usr/local/bin/orchestrator ; argv[]=/usr/local/bin/orchestrator ; ignore_errors=no }`, "/usr/local/bin/orchestrator", true},
		{`{ path=/opt/my bridge/orchestrator ; argv[]=/opt/my bridge/orchestrator }`, "/opt/my bridge/orchestrator", true},
		{`:"/usr/local/bin/orchestrator"`, "/usr/local/bin/orchestrator", true},
		{"", "", false},
	}
	for _, tc := range cases {
		got, ok := parseSystemdExecStartPath(tc.in)
		if ok != tc.ok || got != tc.want {
			t.Errorf("parseSystemdExecStartPath(%q) = (%q, %v), want (%q, %v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}
