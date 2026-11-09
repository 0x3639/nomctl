package backup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/execx"
	"github.com/0x3639/nomctl/internal/logx"
	"github.com/0x3639/nomctl/internal/service"
	"github.com/0x3639/nomctl/internal/ui"
)

const CronPath = "/etc/cron.d/nomctl-backup"

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }

// CronEntry preserves the selected configuration without inheriting the login
// environment. --cadence 0 prevents completion time drifting past tomorrow's
// cron firing and inadvertently skipping every second day.
func CronEntry(cfg config.Config, execPath, hostname string) (string, error) {
	if err := cfg.Validate(); err != nil {
		return "", err
	}
	args := []string{"/usr/bin/env", "-i", "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "HOME=/root",
		"NOMCTL_BACKUP_DIR=" + cfg.BackupDir, "NOMCTL_ZNN_DIR=" + cfg.ZnnDir,
		"NOMCTL_SERVICE_NAME=" + cfg.ServiceName, "NOMCTL_LOG_FILE=" + cfg.LogFile,
		"NOMCTL_MIN_FREE_SPACE_KB=" + strconv.FormatInt(cfg.MinFreeSpaceKB, 10),
		execPath, "backup", "--skip-preflight", "--cadence", "0", "--max-backups", "30",
		"--retention-days", strconv.Itoa(cfg.BackupRetentionDays)}
	for i, arg := range args {
		if strings.ContainsAny(arg, "\r\n\x00") {
			return "", fmt.Errorf("cron settings cannot contain newlines or NUL bytes")
		}
		args[i] = shellQuote(arg)
	}
	command := strings.ReplaceAll(strings.Join(args, " "), "%", `\%`)
	hour, minute := ScheduleTime(hostname, cfg.ServiceName, cfg.BackupHour)
	return fmt.Sprintf("# Managed by nomctl: daily local chain backups; retention %d days.\nSHELL=/bin/sh\nPATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin\n%d %d * * * root %s\n", cfg.BackupRetentionDays, minute, hour, command), nil
}

// LocalSchedule installs one daily cron job without taking a backup now. The
// old systemd timer is disabled when migrating, avoiding duplicate schedules.
func LocalSchedule(cfg config.Config) error {
	execPath, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(execPath); err == nil {
		execPath = resolved
	}
	host, _ := os.Hostname()
	return installLocalSchedule(cfg, execPath, host, CronPath)
}

func installLocalSchedule(cfg config.Config, execPath, hostname, path string) error {
	content, err := CronEntry(cfg, execPath, hostname)
	if err != nil {
		return err
	}
	cronUnit, err := ensureCronInstalled()
	if err != nil {
		return err
	}
	// Stage the complete file before changing the existing schedule. Dotfiles
	// are ignored by cron; the final rename publishes a single complete job.
	f, err := os.CreateTemp(filepath.Dir(path), ".nomctl-backup-")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	_, writeErr := f.WriteString(content)
	err = errors.Join(writeErr, f.Chmod(0o644), f.Close())
	if err != nil {
		return err
	}
	if err := service.EnableNow(cronUnit); err != nil {
		return fmt.Errorf("enable backup scheduler %s: %w", cronUnit, err)
	}
	state, err := service.Status(cronUnit)
	if err != nil {
		return err
	}
	if state != service.Active {
		return fmt.Errorf("backup scheduler %s did not start (state: %s); check its service logs and retry", cronUnit, state)
	}
	timerExists, err := service.Exists(TimerName + ".timer")
	if err != nil {
		return err
	}
	wasEnabled := timerExists && service.IsEnabled(TimerName+".timer")
	if timerExists {
		if err := service.Disable(TimerName + ".timer"); err != nil {
			return err
		}
	}
	if err := os.Rename(f.Name(), path); err != nil {
		if wasEnabled {
			err = errors.Join(err, service.EnableNow(TimerName+".timer"))
		}
		return err
	}
	hour, minute := ScheduleTime(hostname, cfg.ServiceName, cfg.BackupHour)
	logx.Success(fmt.Sprintf("Local backups scheduled daily at %02d:%02d (server time), keeping up to %d days; disk limit %d%%. Cron job: %s", hour, minute, cfg.BackupRetentionDays, MaxDiskUsedPercent, path))
	return nil
}

// ensureCronInstalled reuses an existing cron daemon, or installs the cron
// package on supported Debian/Ubuntu hosts. A failed query must not trigger
// package installation, and installation alone does not prove cron is ready.
func ensureCronInstalled() (string, error) {
	unit, err := installedCronUnit()
	if err != nil || unit != "" {
		return unit, err
	}
	if !execx.Exists("apt-get") {
		return "", fmt.Errorf("cron is missing and apt-get is unavailable; automatic installation requires Debian/Ubuntu; install your distribution's cron or cronie package, then retry")
	}
	if err := ui.Step("Installing cron for daily backups...", func() error {
		if err := execx.Run("apt-get", "update", "-qq"); err != nil {
			return fmt.Errorf("refresh package lists before installing cron: %w", err)
		}
		// needrestart may otherwise restart unrelated services after apt runs.
		return execx.New("apt-get", "install", "-y", "--no-install-recommends", "cron").Env("NEEDRESTART_MODE=l").Run()
	}); err != nil {
		return "", fmt.Errorf("install cron for scheduled backups: %w; resolve the package-manager error and retry scheduling", err)
	}
	if err := service.DaemonReload(); err != nil {
		return "", fmt.Errorf("reload systemd after installing cron: %w", err)
	}
	unit, err = installedCronUnit()
	if err != nil {
		return "", err
	}
	if unit == "" {
		return "", fmt.Errorf("cron installation completed but neither cron.service nor crond.service was found; check the cron package and retry scheduling")
	}
	return unit, nil
}

func installedCronUnit() (string, error) {
	for _, unit := range []string{"cron.service", "crond.service"} {
		exists, err := service.Exists(unit)
		if err != nil {
			return "", err
		}
		if exists {
			return unit, nil
		}
	}
	return "", nil
}

// LocalScheduleInstalled also lets backup-stale alerts recognize cron jobs.
func LocalScheduleInstalled() bool {
	data, err := os.ReadFile(CronPath)
	return err == nil && strings.HasPrefix(string(data), "# Managed by nomctl: daily local chain backups;")
}
