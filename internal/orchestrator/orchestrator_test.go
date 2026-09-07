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
