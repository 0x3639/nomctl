// Package orchestrator holds operations for the Zenon orchestrator service,
// which usually runs next to a pillar's node.
package orchestrator

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/logx"
	"github.com/0x3639/nomctl/internal/service"
)

// ResetDirs are the state directories a hard reset deletes. The
// orchestrator rebuilds them from the chain on start.
var ResetDirs = []string{"queues", "events"}

// SyncDirs are the chain-sync directories backed up and transferred between
// signers. Configuration, producer keys, TSS shares and voting stores stay out.
var SyncDirs = []string{"events", "queues"}

// IsSyncDir reports whether name is one of the orchestrator's sync-data
// directories (events or queues).
func IsSyncDir(name string) bool {
	for _, d := range SyncDirs {
		if name == d {
			return true
		}
	}
	return false
}

// SettleDelay is how long a hard reset waits after stopping the service
// before deleting state, matching the original script.
const SettleDelay = 10 * time.Second

// ConfirmText is the warning shown before an interactive hard reset.
const ConfirmText = "This stops the orchestrator, deletes its queues and\nevents directories and starts it again.\n\nThe orchestrator rebuilds them from the chain.\n\nContinue?"

// ErrNotInstalled is returned when the orchestrator unit does not exist.
var ErrNotInstalled = errors.New("the orchestrator service is not installed on this node")

// ErrBadDir is returned for an orchestrator directory that is not a safe
// absolute path to manage.
var ErrBadDir = errors.New("NOMCTL_ORCHESTRATOR_DIR must be an absolute path other than /")

// ErrBadBinaryPath is returned when the orchestrator binary path is not safe.
var ErrBadBinaryPath = errors.New("orchestrator binary path must be an absolute file path")

// Hooks so tests can stub the host.
var (
	stopService   = service.Stop
	startService  = service.Start
	unitExists    = service.Exists
	serviceStatus = service.Status
	writeUnit     = service.WriteUnit
	daemonReload  = service.DaemonReload
	enableUnit    = service.Enable
	unitPathFor   = func(cfg config.Config) string { return cfg.OrchestratorUnitPath() }
	sleep         = time.Sleep
)

// ValidateDir checks that dir is absolute and not the filesystem root, and
// returns it cleaned.
func ValidateDir(dir string) (string, error) {
	dir = strings.TrimSpace(dir)
	if !filepath.IsAbs(dir) {
		return "", fmt.Errorf("%w (got %q)", ErrBadDir, dir)
	}
	clean := filepath.Clean(dir)
	if clean == string(filepath.Separator) {
		return "", fmt.Errorf("%w (got %q)", ErrBadDir, dir)
	}
	return clean, nil
}

// ValidateBinaryPath checks that path is a clean absolute path suitable for
// installing the orchestrator executable.
func ValidateBinaryPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("%w (got %q)", ErrBadBinaryPath, path)
	}
	clean := filepath.Clean(path)
	if clean == string(filepath.Separator) || filepath.Base(clean) == string(filepath.Separator) || filepath.Base(clean) == "." {
		return "", fmt.Errorf("%w (got %q)", ErrBadBinaryPath, path)
	}
	return clean, nil
}

// EnsureDataDir ensures dir exists as an owner-only (0700) regular directory,
// refusing symlinks and non-directories.
func EnsureDataDir(dir string) (string, error) {
	clean, err := ValidateDir(dir)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(clean)
	switch {
	case err == nil:
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", fmt.Errorf("orchestrator directory %s must be a real directory", clean)
		}
	case errors.Is(err, os.ErrNotExist):
		if err := os.MkdirAll(clean, 0o700); err != nil {
			return "", fmt.Errorf("create orchestrator directory %s: %w", clean, err)
		}
	default:
		return "", err
	}
	if err := os.Chmod(clean, 0o700); err != nil {
		return "", fmt.Errorf("set permissions on %s: %w", clean, err)
	}
	return clean, nil
}

// Installed reports whether the orchestrator unit exists.
func Installed(cfg config.Config) (bool, error) {
	return unitExists(cfg.OrchestratorService)
}

// UnitFile renders the systemd unit for the orchestrator service running as
// root so its default state directory matches NOMCTL_ORCHESTRATOR_DIR.
func UnitFile(cfg config.Config) string {
	return fmt.Sprintf(`[Unit]
Description=Zenon bridge orchestrator
After=network-online.target

[Service]
User=root
Group=root
ExecStart=:%[1]s
Restart=always
RestartSec=10
TimeoutStopSec=10s
TimeoutStartSec=10s

[Install]
WantedBy=multi-user.target
`, systemdArg(cfg.OrchestratorBinaryPath()))
}

func systemdArg(value string) string {
	return strings.ReplaceAll(strconv.Quote(value), "%", "%%")
}

// EnsureService creates and enables the orchestrator systemd unit when absent.
// An existing unit file is preserved so operator customizations (such as
// ORCHESTRATOR_PRODUCER_PASSPHRASE) are never overwritten.
func EnsureService(cfg config.Config) (created bool, unitPath string, err error) {
	unitPath = unitPathFor(cfg)
	if st, err := os.Lstat(unitPath); err == nil {
		if st.Mode()&os.ModeSymlink != 0 || !st.Mode().IsRegular() {
			return false, unitPath, fmt.Errorf("orchestrator unit %s must be a regular file", unitPath)
		}
		slog.Info(cfg.OrchestratorUnit() + " already exists; preserving existing unit file")
		return false, unitPath, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, unitPath, err
	}
	slog.Info("Creating " + cfg.OrchestratorUnit() + "…")
	if err := writeUnit(unitPath, UnitFile(cfg)); err != nil {
		return false, unitPath, err
	}
	if err := daemonReload(); err != nil {
		return false, unitPath, err
	}
	if err := enableUnit(cfg.OrchestratorUnit()); err != nil {
		return false, unitPath, err
	}
	logx.Success(cfg.OrchestratorUnit() + " created and enabled")
	return true, unitPath, nil
}

// HardReset ports the hard-reset script: stop the orchestrator, wait for it
// to settle, delete its queues and events, start it again. It refuses when
// the unit is not installed.
func HardReset(cfg config.Config) error {
	dir, err := ValidateDir(cfg.OrchestratorDir)
	if err != nil {
		return err
	}
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
		target := filepath.Join(dir, d)
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
