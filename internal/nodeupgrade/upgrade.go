package nodeupgrade

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"

	"github.com/0x3639/nomctl/internal/backup"
	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/fsx"
	"github.com/0x3639/nomctl/internal/logx"
	"github.com/0x3639/nomctl/internal/service"
)

// Run downloads and verifies before stopping the service. The caller must hold
// the same lock used by backup/restore/deploy until this operation returns.
func Run(ctx context.Context, cfg config.Config, client *Client, release Release) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	if _, err := archiveName(runtime.GOOS, runtime.GOARCH); err != nil {
		return err
	}
	if err := checkInstalled(cfg); err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "nomctl-node-upgrade-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	binary, err := client.Fetch(ctx, release, dir, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return installPrepared(cfg, binary, release.Tag)
}

func checkInstalled(cfg config.Config) error {
	st, err := os.Lstat(cfg.BinaryPath())
	if err != nil {
		return fmt.Errorf("read installed node binary: %w", err)
	}
	if !st.Mode().IsRegular() {
		return fmt.Errorf("installed node binary must be a regular file: %s", cfg.BinaryPath())
	}
	exists, err := service.Exists(cfg.ServiceName)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("%s: %w", cfg.ServiceName, service.ErrNotFound)
	}
	return nil
}

func installPrepared(cfg config.Config, binary, tag string) error {
	finishSignals := protectTransaction()
	defer finishSignals()
	slog.Info("Preparing the pre-upgrade backup (scheduled cadence does not apply)...")
	snapshot, err := backup.Prepare(cfg)
	if err != nil {
		return err
	}
	// Stage both binaries on the target filesystem before stopping the service.
	staged, err := stageCopy(binary, cfg.BinaryPath())
	if err != nil {
		_ = snapshot.Discard()
		return err
	}
	defer func() { _ = os.Remove(staged) }()
	previous, err := stageCopy(cfg.BinaryPath(), cfg.BinaryPath())
	if err != nil {
		_ = snapshot.Discard()
		return err
	}
	defer func() { _ = os.Remove(previous) }()
	state, err := service.Status(cfg.ServiceName)
	if err != nil {
		_ = snapshot.Discard()
		return err
	}
	recoverOriginal := func(cause error) error {
		if state.Running() {
			slog.Info("Restarting the original node after upgrade failure...")
			cause = errors.Join(cause, service.Start(cfg.ServiceName))
		}
		return cause
	}
	slog.Info("Stopping " + cfg.ServiceName + " for the backup and upgrade...")
	// Stop also cancels pending starts and verifies the terminal state before
	// any chain data is copied or the installed binary is replaced.
	if err := service.Stop(cfg.ServiceName); err != nil {
		_ = snapshot.Discard()
		return recoverOriginal(err)
	}

	slog.Info("Copying existing chain data into " + snapshot.Path() + "...")
	if err := snapshot.Capture(); err != nil {
		_ = snapshot.Discard()
		return recoverOriginal(err)
	}
	finish := func(operationErr error) error {
		slog.Info("Finishing the pre-upgrade backup archive and SHA-256 sidecar...")
		archive, backupErr := snapshot.Finish()
		if archive != "" {
			slog.Info("Pre-upgrade backup: " + archive)
		}
		return errors.Join(operationErr, backupErr)
	}
	slog.Info("Installing the prepared node binary at " + cfg.BinaryPath() + " (keeping .previous)...")
	if err := os.Rename(previous, cfg.BinaryPath()+".previous"); err != nil {
		return finish(recoverOriginal(fmt.Errorf("save previous binary: %w", err)))
	}
	if err := os.Rename(staged, cfg.BinaryPath()); err != nil {
		return finish(recoverOriginal(fmt.Errorf("install node binary: %w", err)))
	}
	slog.Info("Starting " + cfg.ServiceName + " with release " + tag + "...")
	startErr := service.Start(cfg.ServiceName)
	if startErr == nil {
		slog.Info("Checking that " + cfg.ServiceName + " is active...")
		st, err := service.Status(cfg.ServiceName)
		startErr = err
		if err == nil && st != service.Active {
			startErr = fmt.Errorf("service is %s after start", st)
		}
	}
	if startErr != nil {
		startErr = fmt.Errorf("new node could not be started: %w; previous binary: %s.previous; restore the pre-upgrade data before any downgrade", startErr, cfg.BinaryPath())
	}
	if err := finish(startErr); err != nil {
		if startErr == nil {
			return fmt.Errorf("node %s is running, but backup finalization failed: %w", tag, err)
		}
		return err
	}
	st, err := service.Status(cfg.ServiceName)
	if err != nil {
		return fmt.Errorf("backup complete, but cannot confirm the upgraded service state: %w", err)
	}
	if st != service.Active {
		return fmt.Errorf("backup complete, but %s is %s; previous binary: %s.previous", cfg.ServiceName, st, cfg.BinaryPath())
	}
	logx.Success(fmt.Sprintf("Node upgraded to %s; service is active and backup is complete", tag))
	return nil
}

// protectTransaction defers process termination until the transaction has
// finished or recovered. Child processes still receive terminal signals and
// return failures through the ordinary recovery paths.
func protectTransaction() func() {
	signals := make(chan os.Signal, 1)
	done := make(chan struct{})
	stopped := make(chan struct{})
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	go func() {
		defer close(stopped)
		for {
			select {
			case sig := <-signals:
				slog.Warn("Received " + sig.String() + "; waiting for the upgrade or recovery to finish safely...")
			case <-done:
				return
			}
		}
	}()
	return func() {
		signal.Stop(signals)
		close(done)
		<-stopped
	}
}

func stageCopy(src, target string) (string, error) {
	f, err := os.CreateTemp(filepath.Dir(target), ".nomctl-node-")
	if err != nil {
		return "", err
	}
	path := f.Name()
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	if err := fsx.CopyFile(src, path, 0o755); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}
