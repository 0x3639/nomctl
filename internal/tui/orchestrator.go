package tui

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/huh"

	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/nodeconfig"
	"github.com/0x3639/nomctl/internal/orchestrator"
	"github.com/0x3639/nomctl/internal/service"
	"github.com/0x3639/nomctl/internal/ui"
)

// PickOrchestratorRepo lets the user choose a GitHub account or custom
// repository, defaulting to cfg.OrchestratorRepo (0x3639/orchestrator).
func PickOrchestratorRepo(cfg config.Config) (string, error) {
	defaultRepo, err := orchestrator.NormalizeRepo(cfg.OrchestratorRepo)
	if err != nil {
		defaultRepo = orchestrator.DefaultRepo
	}
	opts := make([]huh.Option[string], 0, len(orchestrator.RepoChoices)+2)
	seen := map[string]bool{}
	for _, c := range orchestrator.RepoChoices {
		if c.Repo == defaultRepo {
			opts = append(opts, huh.NewOption(fmt.Sprintf("%s → %s", c.Label, c.URL), c.Repo))
			seen[c.Repo] = true
		}
	}
	if !seen[defaultRepo] {
		opts = append(opts, huh.NewOption(fmt.Sprintf("configured default → %s", orchestrator.RepoURL(defaultRepo)), defaultRepo))
		seen[defaultRepo] = true
	}
	for _, c := range orchestrator.RepoChoices {
		if !seen[c.Repo] {
			opts = append(opts, huh.NewOption(fmt.Sprintf("%s → %s", c.Label, c.URL), c.Repo))
			seen[c.Repo] = true
		}
	}
	opts = append(opts, huh.NewOption("custom → Enter a GitHub account, owner/repo, or URL", "custom"))

	chosen, err := Select("SELECT ORCHESTRATOR GITHUB ACCOUNT / REPOSITORY:", opts)
	if err != nil {
		return "", err
	}
	if chosen == "custom" {
		custom, err := Input("GitHub account, owner/repo, or URL (blank: "+orchestrator.RepoURL(defaultRepo)+")", orchestrator.RepoURL(defaultRepo))
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(custom) == "" {
			return defaultRepo, nil
		}
		return orchestrator.NormalizeRepo(custom)
	}
	return chosen, nil
}

// PickOrchestratorRelease presents all published releases, defaulting to the
// most recent (the first entry).
func PickOrchestratorRelease(repo string, releases []orchestrator.Release) (string, error) {
	if len(releases) == 0 {
		return "", errors.New("no published releases available")
	}
	options := make([]huh.Option[string], 0, len(releases))
	for i, r := range releases {
		ts := r.PublishedAt
		if ts.IsZero() {
			ts = r.CreatedAt
		}
		label := fmt.Sprintf("%s · %s", r.Tag, ts.Format("2006-01-02"))
		if r.Prerelease {
			label += " (prerelease)"
		}
		if i == 0 {
			label += " (most recent)"
		}
		options = append(options, huh.NewOption(label, r.Tag))
	}
	chosen := releases[0].Tag
	sel := huh.NewSelect[string]().
		Title(ui.StyleHeader.Render("SELECT ORCHESTRATOR RELEASE (" + repo + "):")).
		Options(options...).
		Value(&chosen).
		Height(12)
	if err := runForm(sel); err != nil {
		return "", err
	}
	return chosen, nil
}

// ChooseOrchestratorRelease lists releases from client and either selects tag
// or prompts interactively when pick is true and tag is empty.
func ChooseOrchestratorRelease(ctx context.Context, client *orchestrator.Client, tag string, pick bool) (orchestrator.Release, error) {
	var releases []orchestrator.Release
	if err := ui.Step("Fetching releases from "+client.Repo()+"…", func() error {
		var err error
		releases, err = client.List(ctx)
		return err
	}); err != nil {
		return orchestrator.Release{}, err
	}
	tag = strings.TrimSpace(tag)
	if tag == "" && pick {
		var err error
		tag, err = PickOrchestratorRelease(client.Repo(), releases)
		if err != nil {
			return orchestrator.Release{}, err
		}
	}
	return orchestrator.SelectRelease(releases, tag)
}

// ConfirmOrchestratorDeploy asks for confirmation before installing or upgrading.
func ConfirmOrchestratorDeploy(cfg config.Config, repo string, release orchestrator.Release, upgrade bool) (bool, error) {
	verb := "Install"
	extra := "creates " + cfg.OrchestratorUnit() + " and starter config.json if absent, and starts it."
	if upgrade {
		verb = "Upgrade"
		extra = "keeps the existing service unit and previous binary as .previous, and restarts it."
	}
	return Confirm(fmt.Sprintf("%s orchestrator %s from %s?\n\nDownloads and verifies SHA-256, stops %s, installs at %s,\n%s",
		verb, release.Tag, orchestrator.RepoURL(repo), cfg.OrchestratorService, cfg.OrchestratorBinaryPath(), extra))
}

// OrchestratorInstall runs the interactive orchestrator deployment flow.
func OrchestratorInstall(cfg config.Config) error {
	if err := setupPreflight(cfg); err != nil {
		return err
	}
	repo, err := PickOrchestratorRepo(cfg)
	if err != nil {
		return err
	}
	client, err := orchestrator.NewClient(repo, cfg.GitHubToken)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	release, err := ChooseOrchestratorRelease(ctx, client, "", true)
	if err != nil {
		return err
	}
	ok, err := ConfirmOrchestratorDeploy(cfg, client.Repo(), release, false)
	if err != nil {
		return err
	}
	if !ok {
		return ErrCancelled
	}
	var res orchestrator.DeployResult
	err = withLock("orchestrator install", func() error {
		var err error
		res, err = orchestrator.Install(ctx, cfg, client, release)
		return err
	})
	if err != nil {
		return err
	}
	printOrchestratorDeployResult(res)
	return nil
}

// OrchestratorUpgrade runs the interactive orchestrator upgrade flow.
func OrchestratorUpgrade(cfg config.Config) error {
	installed, err := orchestrator.Installed(cfg)
	if err != nil {
		return err
	}
	if !installed {
		return orchestrator.ErrNotInstalled
	}
	repo, err := PickOrchestratorRepo(cfg)
	if err != nil {
		return err
	}
	client, err := orchestrator.NewClient(repo, cfg.GitHubToken)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	release, err := ChooseOrchestratorRelease(ctx, client, "", true)
	if err != nil {
		return err
	}
	ok, err := ConfirmOrchestratorDeploy(cfg, client.Repo(), release, true)
	if err != nil {
		return err
	}
	if !ok {
		return ErrCancelled
	}
	var res orchestrator.DeployResult
	err = withLock("orchestrator upgrade", func() error {
		var err error
		res, err = orchestrator.Upgrade(ctx, cfg, client, release)
		return err
	})
	if err != nil {
		return err
	}
	printOrchestratorDeployResult(res)
	return nil
}

func printOrchestratorDeployResult(res orchestrator.DeployResult) {
	fmt.Fprintf(os.Stderr, "\nRepository  %s\nRelease     %s\nBinary      %s\nSHA-256     %s\n",
		orchestrator.RepoURL(res.Repo), res.Tag, res.BinaryPath, res.BinarySHA256)
	if res.PreviousBinary != "" {
		fmt.Fprintf(os.Stderr, "Previous    %s\n", res.PreviousBinary)
	}
	if res.UnitCreated {
		fmt.Fprintf(os.Stderr, "Unit        %s (created and enabled)\n", res.UnitPath)
	}
	if res.ConfigCreated {
		fmt.Fprintf(os.Stderr, "Config      starter config.json created; review URLs, producer key and sync templates\n")
	}
}

// OrchestratorBackupSync backs up orchestrator events/ and queues/ locally.
func OrchestratorBackupSync(cfg config.Config) error {
	installed, err := orchestrator.Installed(cfg)
	if err != nil {
		return err
	}
	if !installed {
		return orchestrator.ErrNotInstalled
	}
	defaultDir := orchestrator.SyncBackupDir(cfg)
	outDir, err := Input("Backup destination directory (blank: "+defaultDir+")", defaultDir)
	if err != nil {
		return err
	}
	if strings.TrimSpace(outDir) == "" {
		outDir = defaultDir
	}
	ok, err := Confirm(fmt.Sprintf("Back up orchestrator sync data (events/ and queues/) to %s?\n\nStops %s briefly to capture a consistent copy, then restarts it before compression.", outDir, cfg.OrchestratorService))
	if err != nil {
		return err
	}
	if !ok {
		return ErrCancelled
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var res orchestrator.SyncBackupResult
	err = withLock("orchestrator backup", func() error {
		var err error
		res, err = orchestrator.BackupSyncData(ctx, cfg, orchestrator.SyncBackupOptions{OutputDir: outDir})
		return err
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "\nArchive   %s\nHash      %s\nSHA-256   %s\nIncluded  %s\n",
		res.ArchivePath, res.HashPath, res.SHA256, strings.Join(res.IncludedDirs, ", "))
	return nil
}

// OrchestratorCopySync copies events/ and queues/ to another server via rsync over SSH.
func OrchestratorCopySync(cfg config.Config) error {
	installed, err := orchestrator.Installed(cfg)
	if err != nil {
		return err
	}
	if !installed {
		return orchestrator.ErrNotInstalled
	}
	host, err := Input("Destination SSH host (for example root@other-server)", "")
	if err != nil {
		return err
	}
	portText, err := migrationInput("SSH port", "22")
	if err != nil {
		return err
	}
	port, err := strconv.Atoi(strings.TrimSpace(portText))
	if err != nil {
		return errors.New("SSH port must be a number")
	}
	remoteDir, err := migrationInput("Remote orchestrator directory", cfg.OrchestratorDir)
	if err != nil {
		return err
	}
	remoteSvc, err := migrationInput("Remote orchestrator systemd service", cfg.OrchestratorService)
	if err != nil {
		return err
	}
	useSudo, err := Confirm("Use noninteractive sudo on the remote server?")
	if err != nil {
		return err
	}
	opts := orchestrator.SyncCopyOptions{
		Host:          host,
		Port:          port,
		RemoteDir:     remoteDir,
		RemoteService: remoteSvc,
		Sudo:          useSudo,
		Progress:      os.Stderr,
	}
	if err := opts.Validate(cfg); err != nil {
		return err
	}
	ok, err := Confirm(fmt.Sprintf("Copy events/ and queues/ from %s to %s:%s?\n\nStops local %s, transfers via rsync over SSH, stops remote %s with a safety backup, installs the sync data, and restarts active services.\nRemote config.json, keys and TSS shares are untouched.",
		cfg.OrchestratorDir, opts.Host, opts.RemoteDir, cfg.OrchestratorService, opts.RemoteService))
	if err != nil {
		return err
	}
	if !ok {
		return ErrCancelled
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var res orchestrator.SyncCopyResult
	err = withLock("orchestrator copy-sync", func() error {
		var err error
		res, err = orchestrator.CopySyncData(ctx, cfg, opts)
		return err
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "\nDestination    %s:%s\nCopied         %s\nRemote backup  %s\n",
		res.Host, res.RemoteDir, strings.Join(res.CopiedDirs, ", "), res.RemoteBackup)
	if res.RemoteUnitFound && !res.RemoteRestarted {
		fmt.Fprintf(os.Stderr, "Remote service %s was stopped before copy and remains stopped.\n", res.RemoteService)
	}
	return nil
}

// OrchestratorEditConfig opens the orchestrator's config.json in $EDITOR with
// JSON and schema validation before atomic save.
func OrchestratorEditConfig(cfg config.Config) error {
	dir, err := orchestrator.ValidateDir(cfg.OrchestratorDir)
	if err != nil {
		return err
	}
	path := orchestrator.ConfigPath(dir)
	return withLock("orchestrator config", func() error {
		outcome, backupPath, err := orchestrator.EditConfig(path, EditorRunner, RetryEdit, time.Now())
		if err != nil {
			return err
		}
		switch outcome {
		case nodeconfig.EditUnchanged:
			slog.Info("No changes.")
			return nil
		case nodeconfig.EditAborted:
			slog.Warn("Edit discarded; orchestrator config.json is unchanged.")
			return nil
		}
		if backupPath != "" {
			ui.Success("orchestrator config.json written (previous file: " + backupPath + ")")
		} else {
			ui.Success("orchestrator config.json written")
		}
		return offerOrchestratorRestart(cfg)
	})
}

// OrchestratorSyncTemplates lets the user apply Alchemy or dRPC sync settings
// independently per EVM network (BNB Chain and Ethereum).
func OrchestratorSyncTemplates(cfg config.Config) error {
	dir, err := orchestrator.ValidateDir(cfg.OrchestratorDir)
	if err != nil {
		return err
	}
	path := orchestrator.ConfigPath(dir)
	if summary, err := orchestrator.ReadNetworkSyncSummary(path); err == nil && len(summary) > 0 {
		fmt.Fprintln(os.Stderr, ui.StyleHeader.Render("Current orchestrator network sync settings:"))
		for _, n := range summary {
			fmt.Fprintf(os.Stderr, "  %-14s FilterQuerySize=%-5d RpcRequestsPerSecond=%-5g RpcBurst=%d (%d URLs)\n",
				n.Name, n.Settings.FilterQuerySize, n.Settings.RpcRequestsPerSecond, n.Settings.RpcBurst, n.URLCount)
		}
		fmt.Fprintln(os.Stderr)
	}

	mode, err := Select("APPLY ORCHESTRATOR SYNC TEMPLATES:", []huh.Option[string]{
		huh.NewOption("Choose provider per network (BNB Chain and Ethereum independently)", "per-network"),
		huh.NewOption("Apply dRPC (drpc.org) free tier to both BNB Chain and Ethereum (100/2/3)", "both-drpc"),
		huh.NewOption("Apply Alchemy to both BNB Chain (10/0.5/1) and Ethereum (10/0.5/1)", "both-alchemy"),
		huh.NewOption("Back", "back"),
	})
	if err != nil {
		return err
	}
	assignments := map[string]string{}
	switch mode {
	case "back":
		return nil
	case "both-drpc":
		assignments[orchestrator.NetworkBNBChain] = orchestrator.ProviderDRPC
		assignments[orchestrator.NetworkEthereum] = orchestrator.ProviderDRPC
	case "both-alchemy":
		assignments[orchestrator.NetworkBNBChain] = orchestrator.ProviderAlchemy
		assignments[orchestrator.NetworkEthereum] = orchestrator.ProviderAlchemy
	case "per-network":
		bnbProv, err := Select("BNB Chain sync template:", []huh.Option[string]{
			huh.NewOption("dRPC (drpc.org)  · FilterQuerySize=100,  RpcRequestsPerSecond=2,   RpcBurst=3", orchestrator.ProviderDRPC),
			huh.NewOption("Alchemy          · FilterQuerySize=10,   RpcRequestsPerSecond=0.5, RpcBurst=1", orchestrator.ProviderAlchemy),
			huh.NewOption("Skip (keep current BNB Chain settings)", "skip"),
		})
		if err != nil {
			return err
		}
		if bnbProv != "skip" {
			assignments[orchestrator.NetworkBNBChain] = bnbProv
		}
		ethProv, err := Select("Ethereum sync template:", []huh.Option[string]{
			huh.NewOption("dRPC (drpc.org)  · FilterQuerySize=100,  RpcRequestsPerSecond=2,   RpcBurst=3", orchestrator.ProviderDRPC),
			huh.NewOption("Alchemy          · FilterQuerySize=10,   RpcRequestsPerSecond=0.5, RpcBurst=1", orchestrator.ProviderAlchemy),
			huh.NewOption("Skip (keep current Ethereum settings)", "skip"),
		})
		if err != nil {
			return err
		}
		if ethProv != "skip" {
			assignments[orchestrator.NetworkEthereum] = ethProv
		}
		if len(assignments) == 0 {
			slog.Info("No sync templates selected.")
			return nil
		}
	}

	var res orchestrator.TemplateApplyResult
	err = withLock("orchestrator template", func() error {
		var err error
		res, err = orchestrator.ApplySyncTemplates(path, assignments, time.Now())
		return err
	})
	if err != nil {
		return err
	}
	for _, tpl := range res.Applied {
		ui.Success(fmt.Sprintf("%s → %s (FilterQuerySize=%d, RpcRequestsPerSecond=%g, RpcBurst=%d)",
			tpl.Network, tpl.DisplayName, tpl.Settings.FilterQuerySize, tpl.Settings.RpcRequestsPerSecond, tpl.Settings.RpcBurst))
	}
	if res.BackupPath != "" {
		fmt.Fprintf(os.Stderr, "Previous config.json saved as %s\n", res.BackupPath)
	}
	return offerOrchestratorRestart(cfg)
}

func offerOrchestratorRestart(cfg config.Config) error {
	if !service.IsActive(cfg.OrchestratorService) {
		return nil
	}
	ok, err := Confirm(cfg.OrchestratorService + " reads config.json at start. Restart it now?")
	if err != nil || !ok {
		return err
	}
	return service.Restart(cfg.OrchestratorService)
}
