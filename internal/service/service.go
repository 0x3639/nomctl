// Package service controls the node's systemd unit by shelling out to
// systemctl and journalctl (ports of lib/start.sh, stop.sh, restart.sh and
// monitor.sh).
package service

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/0x3639/nomctl/internal/execx"
	"github.com/0x3639/nomctl/internal/logx"
)

// ErrNotFound is returned when the unit does not exist.
var ErrNotFound = errors.New("service does not exist")

// IsActive reports whether the unit is active (systemctl is-active --quiet).
func IsActive(name string) bool {
	return execx.New("systemctl", "is-active", "--quiet", name).Quiet() == nil
}

// Exists reports whether systemd knows the unit. `systemctl status` exits
// with 4 when the unit cannot be found, which is what the bash version checks.
func Exists(name string) bool {
	err := execx.New("systemctl", "status", name).Quiet()
	return err == nil || execx.ExitCode(err) != 4
}

// Start starts the unit unless it is already running.
func Start(name string) error {
	if !Exists(name) {
		return fmt.Errorf("%s: %w", name, ErrNotFound)
	}
	if IsActive(name) {
		slog.Info(name + " service is already running")
		return nil
	}
	if err := execx.Run("systemctl", "start", name); err != nil {
		return fmt.Errorf("failed to start %s service: %w", name, err)
	}
	logx.Success(name + " service started successfully")
	return nil
}

// Stop stops the unit if it is running.
func Stop(name string) error {
	if !IsActive(name) {
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
	if !IsActive(name) {
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
	var ee *execx.Error
	if !errors.As(err, &ee) {
		return false
	}
	code := execx.ExitCode(err)
	// journalctl exits 130 (or -1 when killed by a signal) on Ctrl+C.
	return code == 130 || code == -1 || errors.Is(ee.Err, syscall.EINTR)
}
