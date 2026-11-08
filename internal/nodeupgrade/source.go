package nodeupgrade

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/deploy"
)

// FromSource builds while the node runs, then uses the same backup/install
// transaction as signed releases. The caller must hold the node-operation lock.
func FromSource(ctx context.Context, cfg config.Config, repoURL, branch string) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := checkInstalled(cfg); err != nil {
		return err
	}
	if repoURL == "" {
		repoURL = cfg.RepoURL
	}
	if branch == "" {
		branch = cfg.BranchName
	}
	if err := os.MkdirAll(cfg.WorkDir, 0o755); err != nil {
		return err
	}
	slog.Info("Preparing source build dependencies while the node keeps running...")
	if err := deploy.InstallDependencies(); err != nil {
		return err
	}
	slog.Info("Preparing the Go toolchain...")
	if err := deploy.InstallGo(cfg); err != nil {
		return err
	}
	dir, err := os.MkdirTemp(cfg.WorkDir, "node-upgrade-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	binary, commit, err := deploy.BuildSource(ctx, cfg, repoURL, branch, filepath.Join(dir, "source"))
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	slog.Info(fmt.Sprintf("Source build ready: %s at %s (commit %s)", repoURL, branch, commit))
	return installPrepared(cfg, binary, "source commit "+commit)
}
