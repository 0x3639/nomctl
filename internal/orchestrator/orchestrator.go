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
	"github.com/0x3639/nomctl/internal/execx"
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
	unitLoaded    = defaultUnitLoaded
	serviceStatus = service.Status
	writeUnit     = service.WriteUnit
	daemonReload  = service.DaemonReload
	enableUnit    = service.Enable
	unitExecPath  = defaultUnitExecPath
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

// ValidateGeneratedUnitDir verifies that dir can be selected by a generated
// orchestrator systemd unit. Upstream orchestrator always reads $HOME/.orchestrator,
// so a generated unit selects dir by setting Environment="HOME=<parent>".
func ValidateGeneratedUnitDir(dir string) (homeDir string, err error) {
	clean, err := ValidateDir(dir)
	if err != nil {
		return "", err
	}
	if filepath.Base(clean) != ".orchestrator" {
		return "", fmt.Errorf("generated orchestrator service requires NOMCTL_ORCHESTRATOR_DIR to end with /.orchestrator (got %q) because the orchestrator reads $HOME/.orchestrator", clean)
	}
	return filepath.Dir(clean), nil
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

// EnsureDataDir ensures dir exists as a real non-symlink directory. Newly
// created directories receive mode 0700; existing directories retain their
// permissions so preserved non-root/group-accessible setups stay readable.
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
		return clean, nil
	case errors.Is(err, os.ErrNotExist):
		if err := os.MkdirAll(clean, 0o700); err != nil {
			return "", fmt.Errorf("create orchestrator directory %s: %w", clean, err)
		}
		if err := os.Chmod(clean, 0o700); err != nil {
			return "", fmt.Errorf("set permissions on %s: %w", clean, err)
		}
		return clean, nil
	default:
		return "", err
	}
}

// Installed reports whether the orchestrator unit exists.
func Installed(cfg config.Config) (bool, error) {
	return unitExists(cfg.OrchestratorService)
}

// UnitFile renders the systemd unit for the orchestrator service running as
// root, setting HOME so $HOME/.orchestrator matches cfg.OrchestratorDir.
func UnitFile(cfg config.Config) (string, error) {
	binPath, err := ValidateBinaryPath(cfg.OrchestratorBinaryPath())
	if err != nil {
		return "", err
	}
	homeDir, err := ValidateGeneratedUnitDir(cfg.OrchestratorDir)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(`[Unit]
Description=Zenon bridge orchestrator
After=network-online.target

[Service]
User=root
Group=root
Environment=%[1]s
ExecStart=:%[2]s
Restart=always
RestartSec=10
TimeoutStopSec=10s
TimeoutStartSec=10s

[Install]
WantedBy=multi-user.target
`, systemdArg("HOME="+homeDir), systemdArg(binPath)), nil
}

func systemdArg(value string) string {
	return strings.ReplaceAll(strconv.Quote(value), "%", "%%")
}

// EnsureService creates and enables the orchestrator systemd unit when no unit
// is loaded and no administrator unit file exists. Existing units (including
// vendor units under /usr/lib/systemd/system) are preserved. If writing,
// daemon-reload, or enable fails after creating the file, the newly written
// file is removed so retries remain recoverable.
func EnsureService(cfg config.Config) (created bool, unitPath string, err error) {
	unitPath = unitPathFor(cfg)
	loaded, err := unitLoaded(cfg.OrchestratorService)
	if err != nil {
		return false, unitPath, err
	}
	if loaded {
		slog.Info(cfg.OrchestratorUnit() + " already loaded; preserving existing unit")
		return false, unitPath, nil
	}
	if st, err := os.Lstat(unitPath); err == nil {
		if st.Mode()&os.ModeSymlink != 0 || !st.Mode().IsRegular() {
			return false, unitPath, fmt.Errorf("orchestrator unit %s must be a regular file", unitPath)
		}
		slog.Info(cfg.OrchestratorUnit() + " file already exists; preserving existing unit file")
		return false, unitPath, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, unitPath, err
	}
	content, err := UnitFile(cfg)
	if err != nil {
		return false, unitPath, err
	}
	slog.Info("Creating " + cfg.OrchestratorUnit() + "…")
	if err := writeUnit(unitPath, content); err != nil {
		_ = os.Remove(unitPath)
		return false, unitPath, err
	}
	if err := daemonReload(); err != nil {
		_ = os.Remove(unitPath)
		_ = daemonReload()
		return false, unitPath, err
	}
	if err := enableUnit(cfg.OrchestratorUnit()); err != nil {
		_ = os.Remove(unitPath)
		_ = daemonReload()
		return false, unitPath, err
	}
	logx.Success(cfg.OrchestratorUnit() + " created and enabled")
	return true, unitPath, nil
}

func defaultUnitLoaded(name string) (bool, error) {
	out, err := execx.Output("systemctl", "show", "--property=LoadState", "--value", name)
	if err != nil {
		return false, fmt.Errorf("inspect LoadState of %s: %w", name, err)
	}
	state := strings.TrimSpace(out)
	return state != "" && state != "not-found", nil
}

func defaultUnitExecPath(name string) (string, error) {
	out, err := execx.Output("systemctl", "show", "--property=ExecStart", "--value", name)
	if err != nil {
		return "", fmt.Errorf("inspect ExecStart of %s: %w", name, err)
	}
	trimmed := strings.TrimSpace(out)
	if trimmed == "" {
		return "", fmt.Errorf("unit %s has empty ExecStart", name)
	}
	p, ok := parseSystemdExecStartPath(trimmed)
	if !ok {
		return "", fmt.Errorf("cannot parse ExecStart of %s: %q", name, trimmed)
	}
	return p, nil
}

// parseSystemdExecStartPath extracts the executable path from `systemctl show
// --property=ExecStart --value` output or a raw unit ExecStart= value.
func parseSystemdExecStartPath(out string) (string, bool) {
	s := strings.TrimSpace(out)
	if s == "" {
		return "", false
	}
	if idx := strings.Index(s, "path="); idx >= 0 {
		rest := s[idx+len("path="):]
		if end := strings.Index(rest, " ;"); end >= 0 {
			rest = rest[:end]
		} else if end := strings.IndexAny(rest, ";}\n"); end >= 0 {
			rest = rest[:end]
		}
		p := strings.TrimSpace(rest)
		return p, p != ""
	}
	// Strip systemd ExecStart prefixes (: - @ + !).
	s = strings.TrimLeft(s, ":-@+!")
	s = strings.TrimSpace(s)
	if s == "" {
		return "", false
	}
	if strings.HasPrefix(s, `"`) {
		if end := strings.Index(s[1:], `"`); end >= 0 {
			if unq, err := strconv.Unquote(s[:end+2]); err == nil && unq != "" {
				return unq, true
			}
		}
	}
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return "", false
	}
	return fields[0], true
}

func sameExecutablePath(a, b string) bool {
	cleanA := filepath.Clean(a)
	cleanB := filepath.Clean(b)
	if cleanA == cleanB {
		return true
	}
	resA, errA := filepath.EvalSymlinks(cleanA)
	resB, errB := filepath.EvalSymlinks(cleanB)
	return errA == nil && errB == nil && resA == resB
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
