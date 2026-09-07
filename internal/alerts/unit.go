package alerts

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/execx"
	"github.com/0x3639/nomctl/internal/fsx"
	"github.com/0x3639/nomctl/internal/metrics"
	"github.com/0x3639/nomctl/internal/service"
)

// UnitName is the systemd service that runs the daemon.
const UnitName = "nomctl-alerts"

// UnitPath is where the unit file is written.
const UnitPath = "/etc/systemd/system/" + UnitName + ".service"

// ProbeName is the root-run oneshot that records what the unprivileged
// daemon cannot read from /proc; ProbeTimer runs it every 30 s.
const (
	ProbeName  = "nomctl-alerts-probe"
	ProbePath  = "/etc/systemd/system/" + ProbeName + ".service"
	ProbeTimer = "/etc/systemd/system/" + ProbeName + ".timer"
)

// RunUser is the locked system account the daemon runs as. It owns
// /run/nomctl and can read the pairing config, nothing else.
const RunUser = "nomctl"

// UnitText renders the daemon's unit. The monitoring settings in effect at
// setup time (service name, data and backup directories) are written into
// the unit so the daemon watches the same node the operator configured,
// exactly as the backup timer unit does. The daemon runs unprivileged with
// no capabilities; per-process figures come from the probe.
func UnitText(execPath string, cfg config.Config) string {
	return fmt.Sprintf(`[Unit]
Description=nomctl alerts daemon
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=%[1]s
Group=%[1]s
ExecStart=%[2]s alerts run
Restart=always
RestartSec=10
RuntimeDirectory=nomctl
RuntimeDirectoryPreserve=yes
NoNewPrivileges=true
CapabilityBoundingSet=
AmbientCapabilities=
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictSUIDSGID=true
LockPersonality=true
RestrictRealtime=true
ReadWritePaths=/run/nomctl
Environment=NOMCTL_SKIP_PREFLIGHT=true
Environment=NOMCTL_LOG_FILE=
Environment="NOMCTL_SERVICE_NAME=%[3]s"
Environment="NOMCTL_BINARY_NAME=%[4]s"
Environment="NOMCTL_ZNN_DIR=%[5]s"
Environment="NOMCTL_BACKUP_DIR=%[6]s"

[Install]
WantedBy=multi-user.target
`, RunUser, execPath, unitQuote(cfg.ServiceName), unitQuote(cfg.BinaryName), unitQuote(cfg.ZnnDir), unitQuote(cfg.BackupDir))
}

// ProbeText renders the probe oneshot. It runs as root because counting
// another user's open files needs CAP_SYS_PTRACE, which would also let the
// daemon read the node's memory; confining it to a 30-second oneshot that
// can only write /run/nomctl keeps that out of the long-running process.
// Home is read-only rather than hidden so a data directory under /root can
// be measured for disk space, which the daemon cannot see at all.
func ProbeText(execPath string, cfg config.Config) string {
	return fmt.Sprintf(`[Unit]
Description=nomctl alerts probe (open files and I/O of the node process)

[Service]
Type=oneshot
ExecStart=%s alerts probe
RuntimeDirectory=nomctl
RuntimeDirectoryPreserve=yes
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=read-only
PrivateTmp=true
PrivateNetwork=true
ReadWritePaths=/run/nomctl
Environment=NOMCTL_SKIP_PREFLIGHT=true
Environment=NOMCTL_LOG_FILE=
Environment="NOMCTL_SERVICE_NAME=%s"
Environment="NOMCTL_ZNN_DIR=%s"
`, execPath, unitQuote(cfg.ServiceName), unitQuote(cfg.ZnnDir))
}

// ProbeTimerText renders the probe timer.
func ProbeTimerText() string {
	return `[Unit]
Description=Run the nomctl alerts probe every 30 seconds

[Timer]
OnBootSec=30s
OnUnitActiveSec=30s
AccuracySec=5s

[Install]
WantedBy=timers.target
`
}

// unitQuote escapes a value for a double-quoted systemd setting.
func unitQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, `"`, `\"`)
}

// execStartPath quotes and escapes an executable path for ExecStart= when
// it contains characters systemd would otherwise split or misparse.
func execStartPath(p string) string {
	if strings.ContainsAny(p, " \t\"\\") {
		return `"` + unitQuote(p) + `"`
	}
	return p
}

// Hooks so tests can stub the host.
var (
	lookupUser = user.Lookup
	runUseradd = func(name string) error {
		return execx.Run("useradd", "--system", "--no-create-home", "--shell", "/usr/sbin/nologin", "--user-group", name)
	}
	chown = os.Chown
)

// EnsureUser creates the locked run user if it does not exist and returns
// its uid and gid.
func EnsureUser() (uid, gid int, err error) {
	u, err := lookupUser(RunUser)
	if err != nil {
		var unknown user.UnknownUserError
		if !errors.As(err, &unknown) {
			return 0, 0, err
		}
		slog.Info("Creating system user " + RunUser)
		if err := runUseradd(RunUser); err != nil {
			return 0, 0, fmt.Errorf("create user %s: %w", RunUser, err)
		}
		if u, err = lookupUser(RunUser); err != nil {
			return 0, 0, err
		}
	}
	uid, err = strconv.Atoi(u.Uid)
	if err != nil {
		return 0, 0, err
	}
	gid, err = strconv.Atoi(u.Gid)
	if err != nil {
		return 0, 0, err
	}
	return uid, gid, nil
}

// SecureConfig makes the pairing config readable by the run user only:
// root:nomctl 0640.
func SecureConfig(path string, gid int) error {
	if err := chown(path, 0, gid); err != nil {
		return fmt.Errorf("chown %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o640); err != nil {
		return fmt.Errorf("chmod %s: %w", path, err)
	}
	return nil
}

// SecureRuntimeDir hands /run/nomctl to the run user so the daemon can
// write its state; root writers (status, top, the probe) are unaffected.
func SecureRuntimeDir(dir string, uid, gid int) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := chown(dir, uid, gid); err != nil {
		return fmt.Errorf("chown %s: %w", dir, err)
	}
	// The daemon's own state, written by root on older releases, is handed
	// over too. Other files there are replaced by rename and need nothing.
	if p := filepath.Join(dir, filepath.Base(DefaultStatePath)); fsx.Exists(p) {
		if err := chown(p, uid, gid); err != nil {
			return fmt.Errorf("chown %s: %w", p, err)
		}
	}
	return nil
}

// selfPath is the binary the units should run.
func selfPath() (string, error) {
	execPath, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(execPath); err == nil {
		execPath = resolved
	}
	return execStartPath(execPath), nil
}

// writeIfChanged writes content to path when it differs and reports
// whether it did.
func writeIfChanged(path, content string) (bool, error) {
	if current, err := os.ReadFile(path); err == nil && string(current) == content {
		return false, nil
	}
	return true, service.WriteUnit(path, content)
}

// InstallUnit writes, enables and starts the daemon and the probe: the
// user, file modes and all three unit files are converged, systemd is
// reloaded and the daemon restarted.
func InstallUnit(cfg config.Config) error {
	if _, err := Converge(cfg, DefaultConfigPath); err != nil {
		return err
	}
	if err := service.EnableNow(ProbeName + ".timer"); err != nil {
		return err
	}
	if err := service.EnableNow(UnitName + ".service"); err != nil {
		return err
	}
	// EnableNow is a no-op for a running unit; restart to pick up config.
	return service.RestartUnit(UnitName + ".service")
}

// Converge brings the run user, the config file mode, the runtime
// directory and the unit files to the current layout. It restarts a
// running daemon when a unit changed and reports whether anything was
// written. Root callers use it so nodes from older releases migrate on
// their next upgrade without a manual step.
func Converge(cfg config.Config, configPath string) (bool, error) {
	uid, gid, err := EnsureUser()
	if err != nil {
		return false, err
	}
	if fsx.Exists(configPath) {
		if err := SecureConfig(configPath, gid); err != nil {
			return false, err
		}
	}
	if err := SecureRuntimeDir(filepath.Dir(metrics.DefaultProbePath), uid, gid); err != nil {
		return false, err
	}
	execPath, err := selfPath()
	if err != nil {
		return false, err
	}
	changed := false
	for _, u := range []struct{ path, text string }{
		{UnitPath, UnitText(execPath, cfg)},
		{ProbePath, ProbeText(execPath, cfg)},
		{ProbeTimer, ProbeTimerText()},
	} {
		wrote, err := writeIfChanged(u.path, u.text)
		if err != nil {
			return changed, err
		}
		changed = changed || wrote
	}
	if changed {
		if err := service.DaemonReload(); err != nil {
			return true, err
		}
		if service.IsActive(UnitName) {
			slog.Info("Unit files changed; restarting " + UnitName)
			if err := service.RestartUnit(UnitName + ".service"); err != nil {
				return true, err
			}
		}
	}
	// The timer is checked on every run, not only when a unit changed: a
	// daemon without its probe silently loses fds_high.
	if service.IsEnabled(UnitName+".service") && !service.IsEnabled(ProbeName+".timer") {
		slog.Info("Enabling " + ProbeName + ".timer")
		if err := service.EnableNow(ProbeName + ".timer"); err != nil {
			return changed, err
		}
	}
	return changed, nil
}

// UninstallUnit stops, disables and removes the daemon and probe units. A
// unit that was never installed is not an error; other systemd failures
// are.
func UninstallUnit() error {
	var errs []error
	if fsx.Exists(ProbeTimer) {
		if err := service.Disable(ProbeName + ".timer"); err != nil {
			errs = append(errs, err)
		}
	}
	if fsx.Exists(UnitPath) {
		if err := service.Disable(UnitName + ".service"); err != nil {
			errs = append(errs, err)
		}
	}
	removed := false
	for _, p := range []string{UnitPath, ProbePath, ProbeTimer} {
		if err := os.Remove(p); err == nil {
			removed = true
		} else if !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	if removed {
		if err := service.DaemonReload(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// ReloadDaemon asks a running daemon to re-read its config (SIGHUP).
func ReloadDaemon() error {
	return service.Reload(UnitName + ".service")
}
