package tui

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/lock"
)

func TestServiceActionsRespectOperationLock(t *testing.T) {
	oldChecks := preflightChecks
	preflightChecks = func() error { t.Fatal("service controls must skip setup preflight"); return nil }
	t.Cleanup(func() { preflightChecks = oldChecks })
	oldPath := operationLockPath
	operationLockPath = filepath.Join(t.TempDir(), "nomctl.lock")
	t.Cleanup(func() { operationLockPath = oldPath })
	cfg := config.Default()
	// No service command should run while another operation holds the lock.
	t.Setenv("PATH", t.TempDir())
	held, err := lock.Acquire(operationLockPath, "backup")
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	for _, command := range []Action{ActionStart, ActionStop, ActionRestart} {
		if err := Dispatch(&cfg, command); err == nil || !strings.Contains(err.Error(), "another nomctl operation is in progress") {
			t.Errorf("service action must stop at the shared lock: %v", err)
		}
	}
}

func TestServiceActionReleasesOperationLockOnError(t *testing.T) {
	oldPath := operationLockPath
	operationLockPath = filepath.Join(t.TempDir(), "nomctl.lock")
	t.Cleanup(func() { operationLockPath = oldPath })
	cfg := config.Default()
	t.Setenv("PATH", t.TempDir())
	for _, command := range []Action{ActionStart, ActionStop, ActionRestart} {
		if err := Dispatch(&cfg, command); err == nil {
			t.Fatal("missing systemctl must fail")
		}
		held, err := lock.Acquire(operationLockPath, "backup")
		if err != nil {
			t.Fatalf("service action left lock held: %v", err)
		}
		held.Release()
	}
	if _, err := os.Stat(operationLockPath); err != nil {
		t.Fatal(err)
	}
}

func TestSetupActionsRunPreflightBeforeWork(t *testing.T) {
	cfg := config.Default()
	oldPath, oldChecks := operationLockPath, preflightChecks
	operationLockPath = filepath.Join(t.TempDir(), "nomctl.lock")
	t.Cleanup(func() { operationLockPath, preflightChecks = oldPath, oldChecks })
	unavailable := errors.New("setup preflight unavailable")
	for _, action := range []Action{ActionDeploy, ActionPillarDep, ActionAnalytics} {
		t.Run(string(action), func(t *testing.T) {
			calls := 0
			preflightChecks = func() error { calls++; return unavailable }
			if err := Dispatch(&cfg, action); !errors.Is(err, unavailable) {
				t.Fatalf("setup did not stop at preflight: %v", err)
			}
			if calls != 1 {
				t.Fatalf("preflight ran %d times", calls)
			}
		})
	}
	// The Pillar submenu calls the same deployment entry point directly.
	preflightChecks = func() error { return unavailable }
	if err := PillarDeploy(cfg); !errors.Is(err, unavailable) {
		t.Fatalf("nested deployment skipped preflight: %v", err)
	}
}

func TestSetupPreflightHonorsSkip(t *testing.T) {
	oldChecks := preflightChecks
	preflightChecks = func() error { t.Fatal("preflight ran despite SkipPreflight"); return nil }
	t.Cleanup(func() { preflightChecks = oldChecks })
	cfg := config.Default()
	cfg.SkipPreflight = true
	if err := setupPreflight(cfg); err != nil {
		t.Fatal(err)
	}
}
