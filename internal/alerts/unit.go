package alerts

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/0x3639/nomctl/internal/service"
)

// UnitName is the systemd service that runs the daemon.
const UnitName = "nomctl-alerts"

// UnitPath is where the unit file is written.
const UnitPath = "/etc/systemd/system/" + UnitName + ".service"

// UnitText renders the daemon's unit for the given nomctl executable.
func UnitText(execPath string) string {
	return fmt.Sprintf(`[Unit]
Description=nomctl alerts daemon
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s alerts run
Restart=always
RestartSec=10
RuntimeDirectory=nomctl
Environment=NOMCTL_SKIP_PREFLIGHT=true

[Install]
WantedBy=multi-user.target
`, execPath)
}

// InstallUnit writes, enables and starts the daemon unit.
func InstallUnit() error {
	execPath, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(execPath); err == nil {
		execPath = resolved
	}
	if err := service.WriteUnit(UnitPath, UnitText(execPath)); err != nil {
		return err
	}
	if err := service.DaemonReload(); err != nil {
		return err
	}
	if err := service.EnableNow(UnitName + ".service"); err != nil {
		return err
	}
	// Restart in case it was already running with the old config.
	return service.RestartUnit(UnitName + ".service")
}

// UninstallUnit stops, disables and removes the daemon unit.
func UninstallUnit() error {
	_ = service.Stop(UnitName)
	if err := service.Disable(UnitName + ".service"); err != nil {
		return err
	}
	if err := os.Remove(UnitPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return service.DaemonReload()
}

// ReloadDaemon asks a running daemon to re-read its config (SIGHUP).
func ReloadDaemon() error {
	return service.Reload(UnitName + ".service")
}
