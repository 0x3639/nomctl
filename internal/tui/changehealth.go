package tui

import (
	"errors"
	"fmt"
	"os"

	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/health"
	"github.com/0x3639/nomctl/internal/restore"
)

// RecoverChange offers one confirmed recovery after the original lock has been
// released. A failed recovery returns to the operator instead of looping.
func RecoverChange(cfg config.Config, changeErr error) error {
	var failed *health.Failure
	if !errors.As(changeErr, &failed) || failed.RecoveryID == "" {
		return changeErr
	}
	fmt.Fprintln(os.Stderr, failed.Error())
	mode := "Restore the matching chain data AND saved znnd."
	if failed.DataOnly {
		mode = "Restore chain data only and keep installed znnd, preserving your data-only choice."
	}
	ok, err := Confirm(fmt.Sprintf("Recover using retained restore point %s?\n%s\nThe node will stop while data is restored, then health will be checked again.", failed.RecoveryID, mode))
	if err != nil {
		return errors.Join(changeErr, err)
	}
	if !ok {
		return changeErr
	}
	return withLock("confirmed recovery after change", func() error {
		return restore.PointWithHealth(cfg, failed.RecoveryID, restore.RestorePointOptions{DataOnly: failed.DataOnly})
	})
}
