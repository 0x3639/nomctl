package restore

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/0x3639/nomctl/internal/backup"
	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/fsx"
)

// RunRestorePoint verifies and installs matching chain data and binary in the
// same stopped-service transaction. A failed binary install rolls the chain
// data back before the previous binary is restarted. A start failure retains
// the restored pair together plus the previous data/binary recovery copies.
func RunRestorePoint(cfg config.Config, id string) error {
	return RunRestorePointWithOptions(cfg, id, RestorePointOptions{})
}

// RestorePointOptions defaults to the matching data/binary pair. DataOnly is an
// explicit choice to retain the installed znnd, which must support the old data.
type RestorePointOptions struct {
	DataOnly bool
}

// RunRestorePointWithOptions restores a complete point using the selected
// binary policy. Data-only restores never read, stage or replace a node binary.
func RunRestorePointWithOptions(cfg config.Config, id string, opts RestorePointOptions) error {
	p, err := backup.ReadRestorePoint(cfg, id)
	if err != nil {
		return err
	}
	if !p.Complete {
		return fmt.Errorf("restore point is incomplete")
	}
	var b *pointBinary
	if !opts.DataOnly {
		if err := backup.VerifyRestorePointBinary(p); err != nil {
			return err
		}
		b, err = preparePointBinary(cfg, p.BinaryPath())
		if err != nil {
			return err
		}
		defer b.cleanup()
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	return run(cfg, p.ArchivePath(), true, b)
}

type pointBinary struct {
	staged, target, previous string
	installed                bool
}

func preparePointBinary(cfg config.Config, source string) (*pointBinary, error) {
	old, err := os.Lstat(cfg.BinaryPath())
	if err != nil {
		return nil, err
	}
	if !old.Mode().IsRegular() {
		return nil, fmt.Errorf("installed binary must be a regular file")
	}
	next, err := os.Stat(source)
	if err != nil {
		return nil, err
	}
	if err := backup.CheckRestoreSpace(cfg, filepath.Dir(cfg.BinaryPath()), old.Size()+next.Size()); err != nil {
		return nil, err
	}
	b := &pointBinary{target: cfg.BinaryPath()}
	b.staged, err = stagePointBinary(source, b.target, ".nomctl-point-binary-")
	if err != nil {
		return nil, err
	}
	b.previous, err = stagePointBinary(b.target, b.target, ".nomctl-before-restore-")
	if err != nil {
		b.cleanup()
		return nil, err
	}
	return b, nil
}

func stagePointBinary(source, target, prefix string) (string, error) {
	f, err := os.CreateTemp(filepath.Dir(target), prefix)
	if err != nil {
		return "", err
	}
	path := f.Name()
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	if err := fsx.CopyFile(source, path, 0o755); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

var installPointBinary = os.Rename

func (b *pointBinary) install() error {
	if err := installPointBinary(b.staged, b.target); err != nil {
		return fmt.Errorf("install restore point binary: %w", err)
	}
	b.installed = true
	return nil
}

func (b *pointBinary) cleanup() {
	if b.staged != "" {
		_ = os.Remove(b.staged)
	}
	if !b.installed && b.previous != "" {
		_ = os.Remove(b.previous)
	}
}
