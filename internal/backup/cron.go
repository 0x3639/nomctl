package backup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/logx"
	"github.com/0x3639/nomctl/internal/service"
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
	cronUnit := ""
	for _, unit := range []string{"cron.service", "crond.service"} {
		exists, err := service.Exists(unit)
		if err != nil {
			return err
		}
		if exists {
			cronUnit = unit
			break
		}
	}
	if cronUnit == "" {
		return fmt.Errorf("cron is not installed; install and enable cron (or cronie), then retry")
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
		return err
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

// LocalScheduleInstalled also lets backup-stale alerts recognize cron jobs.
func LocalScheduleInstalled() bool {
	data, err := os.ReadFile(CronPath)
	return err == nil && strings.HasPrefix(string(data), "# Managed by nomctl: daily local chain backups;")
}
