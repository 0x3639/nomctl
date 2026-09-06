// Package restore ports restore.sh: verify a backup archive against its
// sha256 sidecar, move the current data aside and extract the archive.
package restore

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/0x3639/nomctl/internal/backup"
	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/execx"
	"github.com/0x3639/nomctl/internal/fsx"
	"github.com/0x3639/nomctl/internal/logx"
	"github.com/0x3639/nomctl/internal/service"
	"github.com/0x3639/nomctl/internal/ui"
)

// Resolve turns a --file argument into an archive path. Anything containing
// a path separator is used as given; a bare name (with or without the
// .tar.gz suffix) is looked up in the backup directory.
func Resolve(cfg config.Config, arg string) string {
	if strings.ContainsRune(arg, os.PathSeparator) {
		return arg
	}
	if !strings.HasSuffix(arg, ".tar.gz") {
		arg += ".tar.gz"
	}
	return filepath.Join(cfg.BackupDir, arg)
}

// Verify checks that the archive and its hash sidecar exist and match.
func Verify(archive string) error {
	if !fsx.Exists(archive) {
		return fmt.Errorf("backup file %s not found", archive)
	}
	hashFile := backup.HashPath(archive)
	stored, err := os.ReadFile(hashFile)
	if err != nil {
		return fmt.Errorf("hash file missing for %s", archive)
	}
	slog.Info("Verifying backup integrity…")
	calculated, err := backup.SHA256File(archive)
	if err != nil {
		return err
	}
	if calculated != strings.TrimSpace(string(stored)) {
		return fmt.Errorf("integrity check failed for %s", archive)
	}
	return nil
}

// MoveAside moves the given data folders into the restore directory as
// <folder>.bak.<unix>, keeping a safety copy of what is about to be replaced.
// It returns the folders that were moved, keyed by name, with their new
// paths; on error the map holds what had been moved so far so the caller can
// put it back with MoveBack.
func MoveAside(cfg config.Config, now time.Time, folders []string) (map[string]string, error) {
	restoreDir := backup.RestoreDir(cfg)
	moved := map[string]string{}
	if err := os.MkdirAll(restoreDir, 0o755); err != nil {
		return moved, err
	}
	slog.Info("Backing up existing node directory (safety snapshot)")
	stamp := fmt.Sprint(now.Unix())
	for _, folder := range folders {
		src := filepath.Join(cfg.ZnnDir, folder)
		if !fsx.IsDir(src) {
			continue
		}
		dst := filepath.Join(restoreDir, folder+".bak."+stamp)
		// mv handles the backup directory living on another filesystem.
		if err := execx.Run("mv", src, dst); err != nil {
			return moved, fmt.Errorf("failed to move %s aside; aborting before touching data: %w", folder, err)
		}
		moved[folder] = dst
	}
	return moved, nil
}

// MoveBack returns folders moved by MoveAside to the data directory. It
// keeps going after a failure and reports every folder it could not restore.
func MoveBack(cfg config.Config, moved map[string]string) error {
	var errs []error
	for folder, src := range moved {
		dst := filepath.Join(cfg.ZnnDir, folder)
		if err := execx.Run("mv", src, dst); err != nil {
			errs = append(errs, fmt.Errorf("put back %s from %s: %w", folder, src, err))
		}
	}
	return errors.Join(errs...)
}

// Run restores archive into the node data directory.
func Run(cfg config.Config, archive string) error {
	restoreDir := backup.RestoreDir(cfg)
	if err := os.MkdirAll(restoreDir, 0o755); err != nil {
		return err
	}
	if err := Verify(archive); err != nil {
		return err
	}
	if err := service.Stop(cfg.ServiceName); err != nil {
		return err
	}

	if _, err := MoveAside(cfg, time.Now(), backup.Folders); err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.ZnnDir, 0o755); err != nil {
		return err
	}
	if err := ui.Step("Extracting backup…", func() error {
		return execx.Run("tar", "-xzf", archive, "-C", cfg.ZnnDir)
	}); err != nil {
		return err
	}
	logx.Success(cfg.ServiceName + " data restored successfully.")
	return service.Start(cfg.ServiceName)
}
