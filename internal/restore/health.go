package restore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/0x3639/nomctl/internal/backup"
	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/doctor"
	"github.com/0x3639/nomctl/internal/health"
)

var observeChange = health.Observe
var checkChange = health.Check
var preserveBeforeRestore = func(cfg config.Config) (string, error) {
	archive, err := backup.RunRestorePoint(cfg, "Before restoring chain data")
	if err != nil {
		return "", err
	}
	if err := Verify(archive); err != nil {
		return "", err
	}
	if _, err := Inspect(archive); err != nil {
		return "", err
	}
	id := filepath.Base(filepath.Dir(archive))
	p, err := backup.ReadRestorePoint(cfg, id)
	if err != nil {
		return "", err
	}
	return id, backup.VerifyRestorePointBinary(p)
}

// WithHealth is the operator-facing restore transaction. Validate the requested
// input first, then retain the current matching pair before any data is changed.
// The caller holds the operation lock across preservation, restore and checks.
func WithHealth(cfg config.Config, archive string) error {
	if err := Verify(archive); err != nil {
		return err
	}
	if _, err := Inspect(archive); err != nil {
		return err
	}
	expected, err := backup.SHA256File(cfg.BinaryPath())
	if err != nil {
		return err
	}
	return changeWithHealth(cfg, true, expected, func() error { return Run(cfg, archive) })
}

func PointWithHealth(cfg config.Config, id string, opts RestorePointOptions) error {
	p, err := backup.ReadRestorePoint(cfg, id)
	if err != nil {
		return err
	}
	if !p.Complete {
		return fmt.Errorf("restore point is incomplete")
	}
	if err := Verify(p.ArchivePath()); err != nil {
		return err
	}
	if _, err := Inspect(p.ArchivePath()); err != nil {
		return err
	}
	if !opts.DataOnly {
		if err := backup.VerifyRestorePointBinary(p); err != nil {
			return err
		}
	}
	expected := p.BinarySHA256
	if opts.DataOnly {
		expected, err = backup.SHA256File(cfg.BinaryPath())
		if err != nil {
			return err
		}
	}
	return changeWithHealth(cfg, opts.DataOnly, expected, func() error { return RunRestorePointWithOptions(cfg, id, opts) })
}

func changeWithHealth(cfg config.Config, dataOnly bool, expected string, change func() error) error {
	before := observeChange(context.Background(), cfg)
	id, err := preserveBeforeRestore(cfg)
	if err != nil {
		return fmt.Errorf("preserve pre-restore data/znnd pair: %w", err)
	}
	if err := change(); err != nil {
		return &health.Failure{RecoveryID: id, DataOnly: dataOnly, Cause: err, Report: health.Report{Outcome: "failed", Before: before}}
	}
	return finishChange(cfg, before, expected, id, dataOnly)
}

func finishChange(cfg config.Config, before doctor.NodeObservation, expected, id string, dataOnly bool) error {
	r, err := checkChange(context.Background(), cfg, before, expected, health.DefaultWindow)
	health.Write(os.Stderr, r)
	if err != nil {
		return &health.Failure{Report: r, RecoveryID: id, DataOnly: dataOnly, Cause: err}
	}
	return nil
}
