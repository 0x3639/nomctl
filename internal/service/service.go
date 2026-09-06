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
	"os/signal"
	"syscall"

	"github.com/0x3639/nomctl/internal/execx"
	"github.com/0x3639/nomctl/internal/logx"
)

// ErrNotFound is returned when the unit does not exist.
var ErrNotFound = errors.New("service does not exist")

// State is the answer of `systemctl is-active`.
type State int

// Unit states nomctl distinguishes.
const (
	Inactive State = iota
	Active
	NotFound
)

// Status queries the unit state. Exit codes 0 (active), 3 (inactive/failed;
// also what is-active reports for an unknown unit) and 4 (no such unit) are
// answers; anything else, including a missing systemctl, is an error so
// callers do not mistake "unknown" for "stopped".
func Status(name string) (State, error) {
	err := execx.New("systemctl", "is-active", "--quiet", name).Quiet()
	switch {
	case err == nil:
		return Active, nil
	case execx.ExitCode(err) == 3:
		return Inactive, nil
	case execx.ExitCode(err) == 4:
		return NotFound, nil
	default:
		return Inactive, fmt.Errorf("cannot determine state of %s: %w", name, err)
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
	return st == Active
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

// Stop stops the unit if it is running. An unknown state is an error so
// that callers about to modify node data do not proceed blindly.
func Stop(name string) error {
	st, err := Status(name)
	if err != nil {
		return err
	}
	if st != Active {
		slog.Info(name + " service is not running")
		return nil
	}
	if err := execx.Run("systemctl", "stop", name); err != nil {
		return fmt.Errorf("failed to stop %s service: %w", name, err)
	}
	logx.Success(name + " service stopped successfully")
	return nil
}

// StopIfRunning is Stop with the wording used during deploy.
func StopIfRunning(name string) error {
	st, err := Status(name)
	if err != nil {
		return err
	}
	if st != Active {
		slog.Info(name + " service is not running")
		return nil
	}
	slog.Info("Stopping " + name + " service...")
	if err := execx.Run("systemctl", "stop", name); err != nil {
		return fmt.Errorf("failed to stop %s service: %w", name, err)
	}
	logx.Success(name + " service stopped")
	return nil
}

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
	// Ctrl+C is meant for journalctl; keep nomctl alive so it exits cleanly.
	signal.Ignore(os.Interrupt)
	defer signal.Reset(os.Interrupt)
	err := execx.New("journalctl", "-u", unit, "-f", "--no-pager").Interactive()
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
