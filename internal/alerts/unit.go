package alerts

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/service"
)

// UnitName is the systemd service that runs the daemon.
const UnitName = "nomctl-alerts"

// UnitPath is where the unit file is written.
const UnitPath = "/etc/systemd/system/" + UnitName + ".service"

// UnitText renders the daemon's unit. The monitoring settings in effect at
// setup time (service name, data and backup directories, log file) are
// written into the unit so the daemon watches the same node the operator
// configured, exactly as the backup timer unit does.
func UnitText(execPath string, cfg config.Config) string {
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
Environment="NOMCTL_SERVICE_NAME=%s"
Environment="NOMCTL_BINARY_NAME=%s"
Environment="NOMCTL_ZNN_DIR=%s"
Environment="NOMCTL_BACKUP_DIR=%s"
Environment="NOMCTL_LOG_FILE=%s"

[Install]
WantedBy=multi-user.target
`, execPath, unitQuote(cfg.ServiceName), unitQuote(cfg.BinaryName), unitQuote(cfg.ZnnDir), unitQuote(cfg.BackupDir), unitQuote(cfg.LogFile))
}

// unitQuote escapes a value for a double-quoted systemd setting.
func unitQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, `"`, `\"`)
}

// InstallUnit writes, enables and starts the daemon unit.
func InstallUnit(cfg config.Config) error {
	execPath, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(execPath); err == nil {
		execPath = resolved
	}
	if strings.ContainsAny(execPath, " \t") {
		execPath = `"` + execPath + `"`
	}
	if err := service.WriteUnit(UnitPath, UnitText(execPath, cfg)); err != nil {
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

// UninstallUnit stops, disables and removes the daemon unit. A unit that
// was never installed is not an error; other systemd failures are.
func UninstallUnit() error {
	if _, err := os.Stat(UnitPath); errors.Is(err, os.ErrNotExist) {
		return nil
	}
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
