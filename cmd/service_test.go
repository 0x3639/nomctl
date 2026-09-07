package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/lock"
	"github.com/spf13/cobra"
)

func TestServiceActionsRespectOperationLock(t *testing.T) {
	oldPath := operationLockPath
	operationLockPath = filepath.Join(t.TempDir(), "nomctl.lock")
	t.Cleanup(func() { operationLockPath = oldPath })
	oldConfig := cfg
	cfg = config.Default()
	t.Cleanup(func() { cfg = oldConfig })
	// No service command should run while another operation holds the lock.
	t.Setenv("PATH", t.TempDir())
	held, err := lock.Acquire(operationLockPath, "backup")
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	for _, command := range []*cobra.Command{startCmd, stopCmd, restartCmd} {
		if err := command.RunE(command, nil); err == nil || !strings.Contains(err.Error(), "another nomctl operation is in progress") {
			t.Errorf("service action must stop at the shared lock: %v", err)
		}
	}
}

func TestServiceActionReleasesOperationLockOnError(t *testing.T) {
	oldPath := operationLockPath
	operationLockPath = filepath.Join(t.TempDir(), "nomctl.lock")
	t.Cleanup(func() { operationLockPath = oldPath })
	oldConfig := cfg
	cfg = config.Default()
	t.Cleanup(func() { cfg = oldConfig })
	t.Setenv("PATH", t.TempDir())
	for _, command := range []*cobra.Command{startCmd, stopCmd, restartCmd} {
		if err := command.RunE(command, nil); err == nil {
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
