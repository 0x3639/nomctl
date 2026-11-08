// Package resync ports resync.sh: wipe the chain data so the node restarts
// from genesis while keeping the wallet and configuration.
package resync

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/fsx"
	"github.com/0x3639/nomctl/internal/logx"
	"github.com/0x3639/nomctl/internal/service"
)

// Dirs are the data directories deleted by a resync. Everything else in the
// data directory (wallet, config.json, ...) is preserved.
var Dirs = []string{"network", "nom", "consensus", "log"}

// ConfirmText is the warning shown before an interactive resync.
const ConfirmText = "This option will delete all local data\nand force a full resync from genesis.\n\nThis is a destructive operation.\n\nAre you sure you want to continue?"

// Run stops the service if running, deletes the chain data and restarts the
// service if it was running before.
func Run(cfg config.Config) error {
	st, err := service.Status(cfg.ServiceName)
	if err != nil {
		return err
	}
	wasActive := st.Running()
	if err := service.Stop(cfg.ServiceName); err != nil {
		return fmt.Errorf("failed to stop %s service; aborting resync: %w", cfg.ServiceName, err)
	}

	deleted, err := Wipe(cfg.ZnnDir)
	if err != nil {
		return err
	}
	if deleted > 0 {
		logx.Success(fmt.Sprintf("Local data erased successfully (%d directories deleted). The node will resync from genesis on next start.", deleted))
	} else {
		slog.Info("No local data found. The node will resync from genesis on next start.")
	}

	if wasActive {
		slog.Info("Restarting " + cfg.ServiceName + " service…")
		if err := service.Start(cfg.ServiceName); err != nil {
			return fmt.Errorf("failed to restart %s service after resync: %w", cfg.ServiceName, err)
		}
	}
	return nil
}

// Wipe removes Dirs under dataDir and returns how many were deleted. Any
// deletion failure is returned after the remaining directories are tried.
func Wipe(dataDir string) (int, error) {
	deleted := 0
	var errs []error
	for _, d := range Dirs {
		target := filepath.Join(dataDir, d)
		if !fsx.IsDir(target) {
			slog.Info(fmt.Sprintf("Directory %s does not exist; skipping", target))
			continue
		}
		if err := os.RemoveAll(target); err != nil {
			errs = append(errs, fmt.Errorf("failed to delete %s: %w", target, err))
			continue
		}
		logx.Success("Deleted " + target)
		deleted++
	}
	return deleted, errors.Join(errs...)
}
