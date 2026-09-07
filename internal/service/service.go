// Package service controls the node's systemd unit by shelling out to
// systemctl and journalctl (ports of lib/start.sh, stop.sh, restart.sh and
// monitor.sh).
package service

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/0x3639/nomctl/internal/execx"
	"github.com/0x3639/nomctl/internal/logx"
)

// ErrNotFound is returned when the unit does not exist.
var ErrNotFound = errors.New("service does not exist")

// State is the active state reported by systemd.
type State int

// Unit states nomctl distinguishes.
const (
	Inactive State = iota
	Active
	NotFound
	Failed
	Activating
	Deactivating
	Reloading
	Maintenance
	Refreshing
	Unknown
)

// String names the state the way systemctl does.
func (s State) String() string {
	switch s {
	case Inactive:
		return "inactive"
	case Active:
		return "active"
	case NotFound:
		return "not found"
	case Failed:
		return "failed"
	case Activating:
		return "activating"
	case Deactivating:
		return "deactivating"
	case Reloading:
		return "reloading"
	case Maintenance:
		return "maintenance"
	case Refreshing:
		return "refreshing"
	default:
		return "unknown"
	}
}

// Running reports states in which systemd is running or starting the unit.
func (s State) Running() bool {
	return s == Active || s == Activating || s == Reloading || s == Refreshing
}

// Stopped reports terminal states with no running service processes.
func (s State) Stopped() bool {
	return s == Inactive || s == Failed || s == NotFound
}

// Status reads the load and active states separately. Exit codes alone do
// not distinguish a stopped unit from a transition or a missing unit.
// Unrecognized output or command failures are errors, never a stopped state.
func Status(name string) (State, error) {
	out, err := execx.Output("systemctl", "show", "--property=LoadState", "--property=ActiveState", name)
	if err != nil {
		return Unknown, fmt.Errorf("cannot determine state of %s: %w", name, err)
	}
	var loadState, activeState string
	for _, line := range strings.Split(out, "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch key {
		case "LoadState":
			loadState = value
		case "ActiveState":
			activeState = value
		}
	}
	if loadState == "not-found" && activeState == "inactive" {
		return NotFound, nil
	}
	if loadState == "" {
		return Unknown, fmt.Errorf("cannot determine state of %s: missing LoadState", name)
	}
	switch activeState {
	case "active":
		return Active, nil
	case "inactive":
		return Inactive, nil
	case "failed":
		return Failed, nil
	case "activating":
		return Activating, nil
	case "deactivating":
		return Deactivating, nil
	case "reloading":
		return Reloading, nil
	case "maintenance":
		return Maintenance, nil
	case "refreshing":
		return Refreshing, nil
	default:
		return Unknown, fmt.Errorf("cannot determine state of %s: unexpected ActiveState %q", name, activeState)
	}
}

// IsActive reports whether the unit is active. Query failures count as not
// active and are logged; use Status when the distinction matters.
func IsActive(name string) bool {
	st, err := Status(name)
	if err != nil {
		slog.Warn(err.Error())
		return false
	}
	return st == Active || st == Reloading || st == Refreshing
}

// Exists reports whether systemd knows the unit. `systemctl status` exits
// with 4 when the unit cannot be found (the check the bash version used);
// 0 and 3 mean the unit exists. Other failures are returned as errors.
func Exists(name string) (bool, error) {
	err := execx.New("systemctl", "status", name).Quiet()
	switch code := execx.ExitCode(err); {
	case err == nil, code == 3:
		return true, nil
	case code == 4:
		return false, nil
	default:
		return false, fmt.Errorf("cannot query unit %s: %w", name, err)
	}
}

// Available checks that systemctl and journalctl are on PATH.
func Available() error {
	for _, tool := range []string{"systemctl", "journalctl"} {
		if !execx.Exists(tool) {
			return fmt.Errorf("%s not found; nomctl requires a systemd-based Linux host", tool)
		}
	}
	return nil
}

// Start starts the unit unless it is already running.
func Start(name string) error {
	exists, err := Exists(name)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("%s: %w", name, ErrNotFound)
	}
	st, err := Status(name)
	if err != nil {
		return err
	}
	if st == Active {
		slog.Info(name + " service is already running")
		return nil
	}
	if err := execx.Run("systemctl", "start", name); err != nil {
		return fmt.Errorf("failed to start %s service: %w", name, err)
	}
	logx.Success(name + " service started successfully")
	return nil
}

// Stop synchronously stops the unit and verifies that it reached a terminal
// state. Even an inactive unit gets a stop job to cancel any pending start.
// Callers about to modify node data must hold the operation lock throughout
// this call and the data operation.
func Stop(name string) error {
	st, err := Status(name)
	if err != nil {
		return err
	}
	if st == NotFound {
		slog.Info(name + " service does not exist")
		return nil
	}
	if err := execx.Run("systemctl", "stop", name); err != nil {
		return fmt.Errorf("failed to stop %s service: %w", name, err)
	}
	st, err = Status(name)
	if err != nil {
		return err
	}
	if !st.Stopped() {
		return fmt.Errorf("%s service did not stop (state: %s)", name, st)
	}
	logx.Success(name + " service stopped successfully")
	return nil
}

// StopIfRunning stops the unit, including any pending start or restart job.
func StopIfRunning(name string) error { return Stop(name) }

// Restart stops then starts the unit.
func Restart(name string) error {
	if err := Stop(name); err != nil {
		return fmt.Errorf("restart failed during stop operation: %w", err)
	}
	if err := Start(name); err != nil {
		return fmt.Errorf("restart failed during start operation: %w", err)
	}
	logx.Success(name + " service restarted successfully")
	return nil
}

// DaemonReload runs systemctl daemon-reload.
func DaemonReload() error { return execx.Run("systemctl", "daemon-reload") }

// Enable enables a unit so it starts at boot.
func Enable(unit string) error { return execx.Run("systemctl", "enable", unit) }

// EnableNow enables and starts a unit.
func EnableNow(unit string) error { return execx.Run("systemctl", "enable", "--now", unit) }

// Disable disables and stops a unit.
func Disable(unit string) error { return execx.Run("systemctl", "disable", "--now", unit) }

// Reload sends SIGHUP to a unit's main process so it re-reads its config.
func Reload(unit string) error { return execx.Run("systemctl", "kill", "-s", "HUP", unit) }

// IsEnabled reports whether the unit is enabled to start at boot.
func IsEnabled(unit string) bool {
	return execx.New("systemctl", "is-enabled", "--quiet", unit).Quiet() == nil
}

// EnsureRunning enables and starts the unit if it is not both enabled and
// active. reload forces a daemon-reload first (after writing a unit file).
func EnsureRunning(unit string, reload bool) error {
	if reload {
		if err := DaemonReload(); err != nil {
			return err
		}
	}
	if IsEnabled(unit) && IsActive(unit) {
		return nil
	}
	return EnableNow(unit)
}

// RestartUnit restarts any unit without the not-running shortcut.
func RestartUnit(unit string) error { return execx.Run("systemctl", "restart", unit) }

// WriteUnit writes a unit file with 0644 permissions.
func WriteUnit(path, content string) error {
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// Logs prints the unit's journal. With follow=true it streams until Ctrl+C;
// otherwise it prints the last `lines` entries.
func Logs(name string, follow bool, lines int) error {
	unit := name + ".service"
	if !follow {
		return execx.New("journalctl", "-u", unit, "-n", fmt.Sprint(lines), "--no-pager").Interactive()
	}
	// Ctrl+C is meant for journalctl: it is forwarded to it and treated as a
	// normal end of the follow.
	err := execx.New("journalctl", "-u", unit, "-f", "--no-pager").InteractiveUntilInterrupt()
	if err != nil && interrupted(err) {
		return nil
	}
	return err
}

func interrupted(err error) bool {
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return false
	}
	// journalctl exits 130 when it handles Ctrl+C itself, or is killed by SIGINT.
	if ee.ExitCode() == 130 {
		return true
	}
	if st, ok := ee.Sys().(syscall.WaitStatus); ok && st.Signaled() && st.Signal() == syscall.SIGINT {
		return true
	}
	return false
}
