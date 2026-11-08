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
// It returns the original state of each prepared folder: its safety-copy
// path, or an empty string if it did not exist. On error the map holds only
// the folders prepared so far, which the caller can roll back with MoveBack.
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
		info, err := os.Lstat(src)
		if errors.Is(err, os.ErrNotExist) {
			moved[folder] = ""
			continue
		}
		if err != nil {
			return moved, fmt.Errorf("inspect %s before moving aside: %w", folder, err)
		}
		if !info.IsDir() {
			return moved, fmt.Errorf("%s is not a data directory", src)
		}
		dst := filepath.Join(restoreDir, folder+".bak."+stamp)
		if fsx.Exists(dst) {
			// mv would nest src inside an existing dst.
			return moved, fmt.Errorf("%s already exists; aborting before touching data", dst)
		}
		// mv handles the backup directory living on another filesystem.
		if err := execx.Run("mv", src, dst); err != nil {
			return moved, fmt.Errorf("failed to move %s aside; aborting before touching data: %w", folder, err)
		}
		moved[folder] = dst
	}
	return moved, nil
}

// MoveBack returns folders moved by MoveAside to the data directory. It is
// the rollback of a failed install, so anything already installed at a
// destination is removed first: mv into an existing directory would nest
// the safety copy inside it. It keeps going after a failure and reports
// every folder it could not restore. An empty source records a folder that
// was originally absent; rollback removes any newly installed replacement.
func MoveBack(cfg config.Config, moved map[string]string) error {
	var errs []error
	for folder, src := range moved {
		dst := filepath.Join(cfg.ZnnDir, folder)
		if err := os.RemoveAll(dst); err != nil {
			errs = append(errs, fmt.Errorf("clear %s before putting the previous data back: %w", dst, err))
			continue
		}
		if src == "" {
			continue
		}
		if err := execx.Run("mv", src, dst); err != nil {
			errs = append(errs, fmt.Errorf("put back %s from %s: %w", folder, src, err))
		}
	}
	return errors.Join(errs...)
}

// Hooks so tests can stub the host.
var (
	stopService  = service.Stop
	startService = service.Start
	installDir   = os.Rename
)

// Run restores archive into the node data directory: verify the hash,
// inspect every entry, extract into a private staging directory while the
// node still runs, then stop, move the current folders aside, rename the
// staged ones into place and start. A failure after the stop puts the
// previous data back.
func Run(cfg config.Config, archive string) error {
	restoreDir := backup.RestoreDir(cfg)
	if err := os.MkdirAll(restoreDir, 0o755); err != nil {
		return err
	}
	if err := Verify(archive); err != nil {
		return err
	}
	manifest, err := Inspect(archive)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.ZnnDir, 0o755); err != nil {
		return err
	}
	now := time.Now()
	staging, err := os.MkdirTemp(cfg.ZnnDir, ".restore-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(staging) }()
	if err := ui.Step("Extracting backup…", func() error { return Extract(archive, staging) }); err != nil {
		return err
	}

	if err := stopService(cfg.ServiceName); err != nil {
		return err
	}
	moved, err := MoveAside(cfg, now, manifest.Folders)
	if err == nil {
		for _, folder := range manifest.Folders {
			if err = installDir(filepath.Join(staging, folder), filepath.Join(cfg.ZnnDir, folder)); err != nil {
				err = fmt.Errorf("install %s: %w", folder, err)
				break
			}
		}
	}
	if err != nil {
		if backErr := MoveBack(cfg, moved); backErr != nil {
			return fmt.Errorf("%w; and the previous data could not all be put back: %w (the node is stopped)", err, backErr)
		}
		if startErr := startService(cfg.ServiceName); startErr != nil {
			return fmt.Errorf("%w; previous data put back but the node did not start: %w", err, startErr)
		}
		return fmt.Errorf("%w; previous data put back and the node restarted", err)
	}
	logx.Success(cfg.ServiceName + " data restored successfully.")
	return startService(cfg.ServiceName)
}
