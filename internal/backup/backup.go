// Package backup ports backup.sh: snapshot the node data directory into a
// tar.gz archive with a sha256 sidecar, prune old archives and schedule
// daily local backups with cron.
package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/execx"
	"github.com/0x3639/nomctl/internal/fsx"
	"github.com/0x3639/nomctl/internal/logx"
	"github.com/0x3639/nomctl/internal/service"
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
		st, err := os.Lstat(m)
		if err != nil || !st.Mode().IsRegular() {
			continue
		}
		// An archive without its hash sidecar never finished; it must not
		// count for cadence or retention decisions.
		if !fsx.Exists(HashPath(m)) {
			slog.Warn("Ignoring incomplete backup " + filepath.Base(m) + " (no hash file)")
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

// Prune enforces both maximum age and count, oldest first.
func Prune(cfg config.Config) error {
	infos, err := List(cfg)
	if err != nil {
		return err
	}
	cutoff := time.Now().Add(-time.Duration(cfg.BackupRetentionDays) * 24 * time.Hour)
	for i := len(infos) - 1; i >= 0; i-- {
		if i >= cfg.MaxBackups || (cfg.BackupRetentionDays > 0 && !infos[i].ModTime.After(cutoff)) {
			if err := removeArchive(infos[i]); err != nil {
				return err
			}
		}
	}
	return nil
}

func removeArchive(old Info) error {
	slog.Info("Removing old backup " + filepath.Base(old.Path))
	if err := os.Remove(old.Path); err != nil {
		return fmt.Errorf("prune backup: %w", err)
	}
	if err := os.Remove(HashPath(old.Path)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("prune hash: %w", err)
	}
	return nil
}

// DeleteBackup removes only a completed archive selected from this service's
// ordinary backup list. The caller holds the operation lock after UI approval.
func DeleteBackup(cfg config.Config, path string) error {
	infos, err := List(cfg)
	if err != nil {
		return err
	}
	for _, info := range infos {
		if info.Path == path {
			return removeArchive(info)
		}
	}
	return fmt.Errorf("backup is no longer in the configured backup list")
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
	if err := cfg.Validate(); err != nil {
		return "", err
	}
	slog.Info(fmt.Sprintf("Using backup directory: %s (Keeping %d copies)", cfg.BackupDir, cfg.MaxBackups))
	for _, d := range []string{cfg.BackupDir, RestoreDir(cfg)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return "", err
		}
	}

	if err := Prune(cfg); err != nil {
		return "", err
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

	snapshot, err := Prepare(cfg)
	if err != nil {
		return "", err
	}
	return runSnapshot(cfg, snapshot)
}

func runSnapshot(cfg config.Config, snapshot *Snapshot) (string, error) {
	ctx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	snapshot.ctx = ctx
	st, err := service.Status(cfg.ServiceName)
	if err != nil {
		_ = snapshot.Discard()
		return "", err
	}
	if err := ctx.Err(); err != nil {
		_ = snapshot.Discard()
		return "", err
	}
	wasActive := st.Running()
	if err := service.Stop(cfg.ServiceName); err != nil {
		_ = snapshot.Discard()
		if wasActive {
			err = errors.Join(err, service.Start(cfg.ServiceName))
		}
		return "", err
	}
	if err := snapshot.Capture(); err != nil {
		_ = snapshot.Discard()
		// Never leave the node down because the snapshot failed.
		if wasActive {
			err = errors.Join(err, service.Start(cfg.ServiceName))
		}
		return "", err
	}
	if wasActive {
		if err := service.Start(cfg.ServiceName); err != nil {
			return "", fmt.Errorf("restart failed; snapshot retained at %s: %w", snapshot.Path(), err)
		}
	}

	return snapshot.Finish()
}

// Snapshot separates the stopped-node copy from online compression. Callers
// must hold the node-operation lock through Prepare, Capture and Finish.
// Failed finalization leaves its unique directory available for recovery.
type Snapshot struct {
	cfg      config.Config
	dir      string
	captured bool
	point    *RestorePoint
	ctx      context.Context
}

// Prepare checks configuration, data and free space before service downtime.
func Prepare(cfg config.Config) (*Snapshot, error) {
	return prepare(cfg, 0)
}

func prepare(cfg config.Config, extraBytes int64) (*Snapshot, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if !fsx.IsDir(cfg.ZnnDir) {
		return nil, fmt.Errorf("cannot access %s", cfg.ZnnDir)
	}
	if err := os.MkdirAll(cfg.BackupDir, 0o755); err != nil {
		return nil, err
	}
	size, err := dataBudget(cfg)
	if err != nil {
		return nil, err
	}
	if err := ensureSpace(cfg, size*2+size/100+extraBytes); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(cfg.BackupDir, "snapshot-")
	if err != nil {
		return nil, err
	}
	return &Snapshot{cfg: cfg, dir: dir, ctx: context.Background()}, nil
}

// Path is the uncompressed recovery copy, retained if Finish fails.
func (s *Snapshot) Path() string { return s.dir }

// Capture copies the existing backup folders. The caller must stop the node.
func (s *Snapshot) Capture() error {
	if s.captured {
		return fmt.Errorf("snapshot already captured")
	}
	// Recheck after stopping: data may have grown since Prepare.
	size, err := dataBudget(s.cfg)
	if err != nil {
		return err
	}
	if err := ensureSpace(s.cfg, size*2+size/100); err != nil {
		return err
	}
	if err := copyData(s.ctx, s.cfg, s.dir); err != nil {
		return err
	}
	s.captured = true
	return nil
}

// Discard removes an incomplete snapshot; never call it on finalization failure.
func (s *Snapshot) Discard() error {
	if s.point != nil {
		if s.point.Complete {
			return fmt.Errorf("completed restore points can only be deleted through the UI")
		}
		return os.RemoveAll(s.point.Dir)
	}
	return os.RemoveAll(s.dir)
}

// Finish creates the ordinary archive/hash pair and applies normal retention.
func (s *Snapshot) Finish() (string, error) {
	if !s.captured {
		return "", fmt.Errorf("snapshot has not been captured")
	}
	if s.point != nil {
		return s.finishRestorePoint()
	}
	// The unique suffix also avoids overwriting two backups in the same second.
	name := strings.TrimSuffix(ArchiveName(s.cfg.ServiceName, time.Now()), ".tar.gz")
	archive := filepath.Join(s.cfg.BackupDir, name+"_"+filepath.Base(s.dir)+".tar.gz")
	if err := writeArchiveGuarded(s.ctx, s.dir, archive, &s.cfg); err != nil {
		return "", fmt.Errorf("finish backup archive (snapshot retained at %s): %w", s.dir, err)
	}
	if err := s.Discard(); err != nil {
		return archive, err
	}
	logx.Success("Backup completed: " + filepath.Base(archive))
	if err := Prune(s.cfg); err != nil {
		return archive, err
	}
	return archive, nil
}

// copyData copies the backup folders from the data directory into dst.
func copyData(ctx context.Context, cfg config.Config, dst string) error {
	slog.Info("Copying node data…")
	for _, folder := range Folders {
		src := filepath.Join(cfg.ZnnDir, folder)
		if !fsx.IsDir(src) {
			slog.Info("Skipping missing " + folder)
			continue
		}
		size, err := copyBudget(src)
		if err != nil {
			return err
		}
		if err := ensureSpace(cfg, size); err != nil {
			return err
		}
		// Preserve ownership/modes without allowing cp to perform unbounded
		// data writes. GNU cp is available on the supported Linux hosts.
		if err := execx.New("cp", "-a", "--attributes-only", src, dst+"/").Context(ctx).Run(); err != nil {
			return fmt.Errorf("failed to copy %s: %w", folder, err)
		}
		if err := copyContents(ctx, cfg, src, filepath.Join(dst, folder)); err != nil {
			return fmt.Errorf("failed to copy %s: %w", folder, err)
		}
	}
	return nil
}

// writeArchive compresses srcDir into archive. The tarball is written under a
// temporary name and its hash sidecar is created before the final rename, so
// an interrupted run never leaves a complete-looking archive behind.
func writeArchive(srcDir, archive string) error {
	return writeArchiveGuarded(context.Background(), srcDir, archive, nil)
}

func writeArchiveGuarded(ctx context.Context, srcDir, archive string, cfg *config.Config) error {
	if cfg != nil {
		size, err := copyBudget(srcDir)
		if err != nil {
			return err
		}
		if err := ensureSpace(*cfg, size+size/100); err != nil {
			return err
		}
	}
	slog.Info("Creating archive " + filepath.Base(archive) + "…")
	partial := archive + ".partial"
	f, err := os.OpenFile(partial, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	var dst io.Writer = f
	if cfg != nil {
		dst = spaceWriter{ctx: ctx, cfg: *cfg, dst: f}
	}
	archiveErr := execx.New("tar", "-czf", "-", ".").Dir(srcDir).Context(ctx).StreamOutput(dst)
	closeErr := f.Close()
	if err := errors.Join(archiveErr, closeErr); err != nil {
		_ = os.Remove(partial)
		return err
	}
	sum, err := SHA256File(partial)
	if err != nil {
		_ = os.Remove(partial)
		return err
	}
	if cfg != nil {
		if err := ensureSpace(*cfg, 4096); err != nil {
			_ = os.Remove(partial)
			return err
		}
	}
	if err := os.WriteFile(HashPath(archive), []byte(sum+"\n"), 0o644); err != nil {
		_ = os.Remove(partial)
		return err
	}
	if err := os.Rename(partial, archive); err != nil {
		_ = os.Remove(partial)
		_ = os.Remove(HashPath(archive))
		return err
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

// InstalledCadence reads the cadence baked into the installed timer's
// service unit, so other components see the scheduled value rather than
// the environment default. ok is false when no timer unit is installed.
func InstalledCadence() (days int, ok bool) {
	data, err := os.ReadFile("/etc/systemd/system/" + TimerName + ".service")
	if err != nil {
		return 0, false
	}
	return ParseCadence(string(data))
}

// ParseCadence extracts "--cadence N" from a unit's ExecStart line.
func ParseCadence(unit string) (int, bool) {
	for _, line := range strings.Split(unit, "\n") {
		if !strings.HasPrefix(line, "ExecStart=") {
			continue
		}
		fields := strings.Fields(line)
		for i, f := range fields {
			if f == "--cadence" && i+1 < len(fields) {
				n, err := strconv.Atoi(fields[i+1])
				return n, err == nil
			}
		}
	}
	return 0, false
}
