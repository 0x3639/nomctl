// Package orchestrator holds operations for the Zenon orchestrator service,
// which usually runs next to a pillar's node.
package orchestrator

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/logx"
	"github.com/0x3639/nomctl/internal/service"
)

// ResetDirs are the state directories a hard reset deletes. The
// orchestrator rebuilds them from the chain on start.
var ResetDirs = []string{"queues", "events"}

// SettleDelay is how long a hard reset waits after stopping the service
// before deleting state, matching the original script.
const SettleDelay = 10 * time.Second

// ConfirmText is the warning shown before an interactive hard reset.
const ConfirmText = "This stops the orchestrator, deletes its queues and\nevents directories and starts it again.\n\nThe orchestrator rebuilds them from the chain.\n\nContinue?"

// ErrNotInstalled is returned when the orchestrator unit does not exist.
var ErrNotInstalled = errors.New("the orchestrator service is not installed on this node")

// Hooks so tests can stub the host.
var (
	stopService  = service.Stop
	startService = service.Start
	unitExists   = service.Exists
	sleep        = time.Sleep
)

// Installed reports whether the orchestrator unit exists.
func Installed(cfg config.Config) (bool, error) {
	return unitExists(cfg.OrchestratorService)
}

// HardReset ports the hard-reset script: stop the orchestrator, wait for it
// to settle, delete its queues and events, start it again. It refuses when
// the unit is not installed.
func HardReset(cfg config.Config) error {
	ok, err := Installed(cfg)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotInstalled
	}
	name := cfg.OrchestratorService
	slog.Info("Stopping " + name + "…")
	if err := stopService(name); err != nil {
		return err
	}
	slog.Info(fmt.Sprintf("Waiting %s for it to settle…", SettleDelay))
	sleep(SettleDelay)

	var errs []error
	for _, d := range ResetDirs {
		target := filepath.Join(cfg.OrchestratorDir, d)
		if _, err := os.Stat(target); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				slog.Info(target + " does not exist; skipping")
				continue
			}
			errs = append(errs, err)
			continue
		}
		if err := os.RemoveAll(target); err != nil {
			errs = append(errs, fmt.Errorf("delete %s: %w", target, err))
			continue
		}
		logx.Success("Deleted " + target)
	}
	if err := errors.Join(errs...); err != nil {
		// Start it anyway: a half-cleared orchestrator beats a stopped one.
		if startErr := startService(name); startErr != nil {
			return fmt.Errorf("%w; and %s did not start: %w", err, name, startErr)
		}
		return fmt.Errorf("%w (%s was started again)", err, name)
	}
	slog.Info("Starting " + name + "…")
	if err := startService(name); err != nil {
		return err
	}
	logx.Success(name + " hard reset complete")
	return nil
}
