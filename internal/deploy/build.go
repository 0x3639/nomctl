package deploy

import (
	"context"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/execx"
)

// BuildSource clones and builds without installing or touching the service.
// srcDir must be absent or empty. It returns the binary and exact source commit.
func BuildSource(ctx context.Context, cfg config.Config, repoURL, branch, srcDir string) (string, string, error) {
	slog.Info(fmt.Sprintf("Cloning branch %s from %s...", branch, repoURL))
	if err := execx.New("git", "clone", "--branch", branch, "--single-branch", "--", repoURL, srcDir).Context(ctx).Run(); err != nil {
		return "", "", fmt.Errorf("clone source: %w", err)
	}
	commit, err := execx.New("git", "-C", srcDir, "rev-parse", "HEAD").Context(ctx).Output()
	if err != nil {
		return "", "", fmt.Errorf("identify source commit: %w", err)
	}
	if _, err := hex.DecodeString(commit); err != nil || (len(commit) != 40 && len(commit) != 64) {
		return "", "", fmt.Errorf("invalid source commit %q", commit)
	}
	if cfg.BinaryName == "" || filepath.Base(cfg.BinaryName) != cfg.BinaryName || strings.HasPrefix(cfg.BinaryName, ".") {
		return "", "", fmt.Errorf("invalid node binary name %q", cfg.BinaryName)
	}
	built := filepath.Join(srcDir, "build", cfg.BinaryName)
	slog.Info(fmt.Sprintf("Building %s from commit %s...", cfg.BinaryName, commit))
	if err := execx.New(cfg.GoBinary(), "build", "-o", built, "./cmd/"+cfg.BinaryName).
		Context(ctx).Dir(srcDir).Env("GO111MODULE=on", "GOWORK=off").Run(); err != nil {
		return "", "", fmt.Errorf("build %s: %w", cfg.BinaryName, err)
	}
	st, err := os.Lstat(built)
	if err != nil {
		return "", "", fmt.Errorf("read built binary: %w", err)
	}
	if !st.Mode().IsRegular() || st.Size() == 0 {
		return "", "", fmt.Errorf("build did not produce a regular, nonempty node binary")
	}
	return built, commit, nil
}
