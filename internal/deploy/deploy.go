// Package deploy ports deploy.sh and build.sh: install build dependencies and
// the Go toolchain, clone and build the node, install the binary and create
// the systemd unit.
package deploy

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/execx"
	"github.com/0x3639/nomctl/internal/fsx"
	"github.com/0x3639/nomctl/internal/logx"
	"github.com/0x3639/nomctl/internal/service"
	"github.com/0x3639/nomctl/internal/ui"
)

// OfficialRepoURL and OfficialBranch are what a Pillar builds: the
// zenon-network repository's master, regardless of NOMCTL_REPO_URL and
// NOMCTL_BRANCH_NAME, which stay for test nodes.
const (
	OfficialRepoURL = "https://github.com/zenon-network/go-zenon.git"
	OfficialBranch  = "master"
)

// RepoChoices lists the repositories offered by the interactive deploy menu.
var RepoChoices = []struct{ Label, URL string }{
	{"zenon-network", "https://github.com/zenon-network/go-zenon.git"},
	{"hypercore-one", "https://github.com/hypercore-one/go-zenon.git"},
}

// Run performs the full deploy: dependencies, Go, clone+build, service, start.
func Run(cfg config.Config, repoURL, branch string) error {
	if repoURL == "" {
		repoURL = cfg.RepoURL
	}
	if branch == "" {
		branch = cfg.BranchName
	}
	if err := os.MkdirAll(cfg.WorkDir, 0o755); err != nil {
		return fmt.Errorf("create work dir: %w", err)
	}
	if err := ui.Step("Installing system dependencies...", InstallDependencies); err != nil {
		return fmt.Errorf("failed to install system dependencies: %w", err)
	}
	if err := ui.Step("Installing Go...", func() error { return InstallGo(cfg) }); err != nil {
		return fmt.Errorf("failed to install Go: %w", err)
	}
	if err := CloneAndBuild(cfg, repoURL, branch); err != nil {
		return fmt.Errorf("failed to build binary: %w", err)
	}
	if err := ui.Step("Configuring service...", func() error { return CreateService(cfg) }); err != nil {
		return fmt.Errorf("failed to configure service: %w", err)
	}
	if err := ui.Step("Starting "+cfg.ServiceName+" service...", func() error { return service.Start(cfg.ServiceName) }); err != nil {
		return fmt.Errorf("failed to start %s service: %w", cfg.ServiceName, err)
	}
	logx.Success(cfg.ServiceName + " service started successfully")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, ui.StyleAccent.Bold(true).Render("# Welcome Home 👽"))
	fmt.Fprintln(os.Stderr)
	return nil
}

// InstallDependencies installs git, make and gcc via apt when missing. The
// bash toolkit could assume git because it had been cloned with it; a
// binary install cannot.
func InstallDependencies() error {
	slog.Info("Installing dependencies...")
	var missing []string
	for _, tool := range []string{"git", "make", "gcc"} {
		if execx.Exists(tool) {
			continue
		}
		slog.Info(fmt.Sprintf("%s could not be found. Installing %s...", tool, tool))
		missing = append(missing, tool)
	}
	if len(missing) == 0 {
		return nil
	}
	if err := execx.Run("apt-get", "update", "-qq"); err != nil {
		return fmt.Errorf("failed to update package lists: %w", err)
	}
	return execx.Run("apt-get", append([]string{"install", "-y"}, missing...)...)
}

// InstallGo downloads the configured Go toolchain into WorkDir/go unless the
// exact version is already present.
func InstallGo(cfg config.Config) error {
	desired := "go" + cfg.GoVersion
	if fsx.Exists(cfg.GoBinary()) {
		current, err := goVersion(cfg.GoBinary())
		if err == nil && current == desired {
			slog.Info(fmt.Sprintf("Go %s already installed – skipping download.", cfg.GoVersion))
			return nil
		}
		slog.Info(fmt.Sprintf("Found Go version %s, expected %s – re-installing.", current, desired))
		if err := fsx.RenameExisting(cfg.GoRoot()); err != nil {
			return err
		}
	}
	url, err := cfg.GoURL()
	if err != nil {
		return err
	}
	slog.Info("Downloading and installing Go...")
	tarball := filepath.Join(cfg.WorkDir, "go.tar.gz")
	if err := fsx.Download(url, tarball, 5*time.Minute); err != nil {
		return fmt.Errorf("failed to download Go from %s: %w", url, err)
	}
	defer func() { _ = os.Remove(tarball) }()
	if err := execx.Run("tar", "-C", cfg.WorkDir, "-xzf", tarball); err != nil {
		return err
	}
	logx.Success("Go installed successfully.")
	return nil
}

// goVersion returns e.g. "go1.23.0" for the given go executable.
func goVersion(goBin string) (string, error) {
	out, err := execx.Output(goBin, "version")
	if err != nil {
		return "", err
	}
	return ParseGoVersion(out), nil
}

// ParseGoVersion extracts the third field of `go version` output.
func ParseGoVersion(out string) string {
	fields := strings.Fields(out)
	if len(fields) < 3 {
		return ""
	}
	return fields[2]
}

// ListBranches returns the remote branches of repoURL with "master" first,
// matching the ordering of the interactive bash menu.
func ListBranches(repoURL string) ([]string, error) {
	out, err := execx.Output("git", "ls-remote", "--heads", repoURL)
	if err != nil {
		return nil, fmt.Errorf("unable to connect to repository: %w", err)
	}
	branches := ParseBranches(out)
	if len(branches) == 0 {
		return nil, fmt.Errorf("no branches found in %s", repoURL)
	}
	return branches, nil
}

// ParseBranches parses `git ls-remote --heads` output, placing master first.
func ParseBranches(out string) []string {
	var branches []string
	hasMaster := false
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		name := strings.TrimPrefix(fields[1], "refs/heads/")
		if name == "master" {
			hasMaster = true
			continue
		}
		branches = append(branches, name)
	}
	sort.Strings(branches)
	if hasMaster {
		branches = append([]string{"master"}, branches...)
	}
	return branches
}

// CloneAndBuild stops the node, clones repoURL@branch into WorkDir, builds the
// node binary with the managed Go toolchain and installs it into InstallDir.
func CloneAndBuild(cfg config.Config, repoURL, branch string) error {
	if err := ui.Step("Stopping "+cfg.ServiceName+" in case it is running...", func() error {
		return service.StopIfRunning(cfg.ServiceName)
	}); err != nil {
		return err
	}
	slog.Info(fmt.Sprintf("Using repository %s with branch %s", repoURL, branch))

	srcDir := cfg.SourceDir()
	if err := fsx.RenameExisting(srcDir); err != nil {
		return fmt.Errorf("failed to prepare directories: %w", err)
	}
	if err := ui.Step(fmt.Sprintf("Cloning branch '%s' from repository...", branch), func() error {
		return execx.Run("git", "clone", "-b", branch, repoURL, srcDir)
	}); err != nil {
		return fmt.Errorf("failed to clone repository: %w", err)
	}

	built := filepath.Join(srcDir, "build", cfg.BinaryName)
	if err := ui.Step("Building "+cfg.BinaryName+"...", func() error {
		return execx.New(cfg.GoBinary(), "build", "-o", built, "./cmd/"+cfg.BinaryName).
			Dir(srcDir).Env("GO111MODULE=on").Run()
	}); err != nil {
		return fmt.Errorf("failed to build %s: %w", cfg.BinaryName, err)
	}

	if err := ui.Step("Installing "+cfg.BinaryName+" binary...", func() error {
		return fsx.CopyFile(built, cfg.BinaryPath(), 0o755)
	}); err != nil {
		return fmt.Errorf("failed to install %s binary: %w", cfg.BinaryName, err)
	}
	logx.Success("Build completed successfully")
	return nil
}

// UnitFile renders the systemd unit for the node service.
func UnitFile(cfg config.Config) string {
	return fmt.Sprintf(`[Unit]
Description=%[1]s service
After=network.target
[Service]
LimitNOFILE=32768
User=root
Group=root
Type=simple
SuccessExitStatus=SIGKILL 9
ExecStart=%[2]s
KillMode=control-group
Restart=on-failure
TimeoutStopSec=10s
TimeoutStartSec=10s
[Install]
WantedBy=multi-user.target
`, cfg.BinaryName, cfg.BinaryPath())
}

// CreateService writes the node unit when it is missing or differs from
// what nomctl renders (so fixes to the unit reach existing nodes on their
// next deploy), then reloads and enables it.
func CreateService(cfg config.Config) error {
	slog.Info(fmt.Sprintf("Checking if %s is already set up...", cfg.ServiceUnit()))
	want := UnitFile(cfg)
	current, err := os.ReadFile(cfg.ServiceUnitPath())
	changed := false
	switch {
	case err == nil && string(current) == want:
		slog.Info(cfg.ServiceUnit() + " is up to date.")
	case err == nil:
		slog.Info("Updating " + cfg.ServiceUnit() + " to the current unit definition...")
		if err := service.WriteUnit(cfg.ServiceUnitPath(), want); err != nil {
			return err
		}
		changed = true
	default:
		slog.Info("Creating " + cfg.ServiceUnit() + "...")
		if err := service.WriteUnit(cfg.ServiceUnitPath(), want); err != nil {
			return err
		}
	}
	if err := service.DaemonReload(); err != nil {
		return err
	}
	if err := service.Enable(cfg.ServiceUnit()); err != nil {
		return err
	}
	// A changed unit only takes effect on the next (re)start; deploy stops
	// the node before building, but apply it here too in case it is running.
	if changed && service.IsActive(cfg.ServiceName) {
		slog.Info("Restarting " + cfg.ServiceUnit() + " to apply the updated unit...")
		if err := service.RestartUnit(cfg.ServiceUnit()); err != nil {
			return err
		}
	}
	logx.Success(cfg.ServiceUnit() + " is set up.")
	return nil
}
