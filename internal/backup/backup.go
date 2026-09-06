// Package backup ports backup.sh: snapshot the node data directory into a
// tar.gz archive with a sha256 sidecar, prune old archives and schedule
// recurring backups with a systemd timer.
package backup

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hypercore-one/nomctl/internal/config"
	"github.com/hypercore-one/nomctl/internal/execx"
	"github.com/hypercore-one/nomctl/internal/fsx"
	"github.com/hypercore-one/nomctl/internal/logx"
	"github.com/hypercore-one/nomctl/internal/service"
)

// Folders are the data directories included in a backup.
var Folders = []string{"nom", "network", "consensus", "cache"}

// Info describes one backup archive.
type Info struct {
	Path    string
	ModTime time.Time
}

// Name returns the archive name without directory or .tar.gz suffix.
func (i Info) Name() string {
	return strings.TrimSuffix(filepath.Base(i.Path), ".tar.gz")
}

// TempDir is the staging directory used while copying node data.
func TempDir(cfg config.Config) string { return filepath.Join(cfg.BackupDir, "temp") }

// RestoreDir holds safety snapshots taken before a restore.
func RestoreDir(cfg config.Config) string { return filepath.Join(cfg.BackupDir, "restore") }

// ArchiveName builds "<service>_backup_MM-DD-YY_HHMMSS.tar.gz".
func ArchiveName(serviceName string, now time.Time) string {
	return fmt.Sprintf("%s_backup_%s.tar.gz", serviceName, now.Format("01-02-06_150405"))
}

// HashPath returns the sidecar path for an archive (.tar.gz -> .hash).
func HashPath(archive string) string {
	return strings.TrimSuffix(archive, ".tar.gz") + ".hash"
}

// List returns the archives for the configured service, newest first.
func List(cfg config.Config) ([]Info, error) {
	pattern := filepath.Join(cfg.BackupDir, cfg.ServiceName+"_backup_*.tar.gz")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}
	var infos []Info
	for _, m := range matches {
		st, err := os.Stat(m)
		if err != nil || !st.Mode().IsRegular() {
			continue
		}
		infos = append(infos, Info{Path: m, ModTime: st.ModTime()})
	}
	SortNewestFirst(infos)
	return infos, nil
}

// SortNewestFirst orders infos by modification time, newest first.
func SortNewestFirst(infos []Info) {
	sort.SliceStable(infos, func(i, j int) bool { return infos[i].ModTime.After(infos[j].ModTime) })
}

// SelectForPruning returns the archives beyond the newest `keep`.
func SelectForPruning(infos []Info, keep int) []Info {
	sorted := append([]Info{}, infos...)
	SortNewestFirst(sorted)
	if keep < 0 {
		keep = 0
	}
	if len(sorted) <= keep {
		return nil
	}
	return sorted[keep:]
}

// Prune deletes archives (and their hash files) beyond MaxBackups.
func Prune(cfg config.Config) error {
	infos, err := List(cfg)
	if err != nil {
		return err
	}
	for _, old := range SelectForPruning(infos, cfg.MaxBackups) {
		slog.Info("Removing old backup " + filepath.Base(old.Path))
		_ = os.Remove(old.Path)
		_ = os.Remove(HashPath(old.Path))
	}
	return nil
}

// CadenceReached reports whether at least `days` whole days have passed
// since last (the bash version compares integer day differences).
func CadenceReached(last, now time.Time, days int) bool {
	diffDays := int(now.Sub(last).Hours() / 24)
	return diffDays >= days
}

// Options tunes a backup run.
type Options struct {
	// Interactive mirrors ZNNSH_INTERACTIVE_MODE: when false and a cadence is
	// configured, the backup is skipped if the last one is too recent.
	Interactive bool
}

// Run creates a backup archive. It returns the archive path, or "" when the
// run was skipped because of the cadence.
func Run(cfg config.Config, opts Options) (string, error) {
	slog.Info(fmt.Sprintf("Using backup directory: %s (Keeping %d copies)", cfg.BackupDir, cfg.MaxBackups))
	for _, d := range []string{cfg.BackupDir, TempDir(cfg), RestoreDir(cfg)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return "", err
		}
	}

	if !opts.Interactive && cfg.BackupCadenceDays > 0 {
		infos, err := List(cfg)
		if err != nil {
			return "", err
		}
		if len(infos) > 0 {
			last := infos[0].ModTime
			if !CadenceReached(last, time.Now(), cfg.BackupCadenceDays) {
				diffDays := int(time.Since(last).Hours() / 24)
				slog.Info(fmt.Sprintf("Skipping backup – cadence %dd not reached (last %dd ago).", cfg.BackupCadenceDays, diffDays))
				return "", nil
			}
		}
	}

	if err := ensureFreeSpace(cfg); err != nil {
		return "", err
	}

	if err := service.Stop(cfg.ServiceName); err != nil {
		return "", err
	}

	archive := filepath.Join(cfg.BackupDir, ArchiveName(cfg.ServiceName, time.Now()))
	tmp := TempDir(cfg)

	slog.Info("Copying node data…")
	if err := clearDir(tmp); err != nil {
		return "", err
	}
	if !fsx.IsDir(cfg.ZnnDir) {
		return "", fmt.Errorf("cannot access %s", cfg.ZnnDir)
	}
	for _, folder := range Folders {
		src := filepath.Join(cfg.ZnnDir, folder)
		if !fsx.IsDir(src) {
			slog.Info("Skipping missing " + folder)
			continue
		}
		if err := execx.Run("cp", "-a", src, tmp+"/"); err != nil {
			return "", fmt.Errorf("failed to copy %s: %w", folder, err)
		}
	}

	if err := service.Start(cfg.ServiceName); err != nil {
		return "", err
	}

	slog.Info("Creating archive " + filepath.Base(archive) + "…")
	if err := execx.New("tar", "-czf", archive, ".").Dir(tmp).Run(); err != nil {
		return "", err
	}
	sum, err := SHA256File(archive)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(HashPath(archive), []byte(sum+"\n"), 0o644); err != nil {
		return "", err
	}
	if err := clearDir(tmp); err != nil {
		return "", err
	}
	logx.Success("Backup completed: " + filepath.Base(archive))

	if err := Prune(cfg); err != nil {
		return archive, err
	}
	return archive, nil
}

func ensureFreeSpace(cfg config.Config) error {
	avail, usedPct, err := fsx.DiskFree(cfg.BackupDir)
	if err != nil {
		return fmt.Errorf("check disk space: %w", err)
	}
	slog.Info(fmt.Sprintf("Disk space: %d MB available (%d%% used)", avail/1024, usedPct))
	if avail >= cfg.MinFreeSpaceKB {
		return nil
	}
	slog.Warn("Low disk space detected. Attempting cleanup...")
	if err := Prune(cfg); err != nil {
		return err
	}
	avail, _, err = fsx.DiskFree(cfg.BackupDir)
	if err != nil {
		return fmt.Errorf("check disk space: %w", err)
	}
	if avail < cfg.MinFreeSpaceKB {
		return fmt.Errorf("insufficient disk space even after cleanup: %d MB available", avail/1024)
	}
	return nil
}

// clearDir removes every entry inside dir, keeping dir itself.
func clearDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// SHA256File returns the hex digest of a file.
func SHA256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// --- Scheduling -----------------------------------------------------------

// TimerName is the systemd timer/service pair that runs scheduled backups.
const TimerName = "nomctl-backup"

// ScheduleTime derives the daily run time. The minute (0-59) and, unless
// overridden, the hour (2-4) come from a sha256 of hostname+service so that
// many nodes do not all back up at the same instant — the same formula as
// setup_cron_job in the bash version. hourOverride < 0 means "derive".
func ScheduleTime(hostname, serviceName string, hourOverride int) (hour, minute int) {
	sum := sha256.Sum256([]byte(hostname + serviceName))
	h := hex.EncodeToString(sum[:])
	m, _ := strconv.ParseInt(h[0:2], 16, 32)
	minute = int(m % 60)
	if hourOverride >= 0 && hourOverride <= 23 {
		hour = hourOverride
	} else {
		x, _ := strconv.ParseInt(h[2:4], 16, 32)
		hour = 2 + int(x%3)
	}
	return hour, minute
}

// TimerUnit renders the systemd timer.
func TimerUnit(hour, minute int) string {
	return fmt.Sprintf(`[Unit]
Description=Scheduled %s backup

[Timer]
OnCalendar=*-*-* %02d:%02d:00
Persistent=true

[Install]
WantedBy=timers.target
`, TimerName, hour, minute)
}

// ServiceUnit renders the oneshot service triggered by the timer.
func ServiceUnit(cfg config.Config, execPath string) string {
	return fmt.Sprintf(`[Unit]
Description=%s backup job

[Service]
Type=oneshot
Environment=NOMCTL_BACKUP_DIR=%s
Environment=NOMCTL_ZNN_DIR=%s
Environment=NOMCTL_SERVICE_NAME=%s
Environment=NOMCTL_LOG_FILE=%s
ExecStart=%s backup --skip-preflight --max-backups %d --cadence %d
`, TimerName, cfg.BackupDir, cfg.ZnnDir, cfg.ServiceName, cfg.LogFile, execPath, cfg.MaxBackups, cfg.BackupCadenceDays)
}

// Schedule installs and enables the backup timer using this executable.
func Schedule(cfg config.Config) error {
	execPath, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(execPath); err == nil {
		execPath = resolved
	}
	host, _ := os.Hostname()
	hour, minute := ScheduleTime(host, cfg.ServiceName, cfg.BackupHour)

	svcPath := "/etc/systemd/system/" + TimerName + ".service"
	timerPath := "/etc/systemd/system/" + TimerName + ".timer"
	if err := service.WriteUnit(svcPath, ServiceUnit(cfg, execPath)); err != nil {
		return err
	}
	if err := service.WriteUnit(timerPath, TimerUnit(hour, minute)); err != nil {
		return err
	}
	if err := service.DaemonReload(); err != nil {
		return err
	}
	if err := service.EnableNow(TimerName + ".timer"); err != nil {
		return err
	}
	logx.Success(fmt.Sprintf("Recurring backups scheduled for %d:%02d via systemd timer (unit: %s)", hour, minute, timerPath))
	return nil
}
