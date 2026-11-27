package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/0x3639/nomctl/internal/logx"
	"github.com/0x3639/nomctl/internal/nodeconfig"
	"github.com/0x3639/nomctl/internal/orchestrator"
	"github.com/0x3639/nomctl/internal/service"
	"github.com/0x3639/nomctl/internal/tui"
	"github.com/0x3639/nomctl/internal/ui"
)

var (
	flagOrchLogsFollow        bool
	flagOrchLogsLines         int
	flagOrchRepo              string
	flagOrchVersion           string
	flagOrchYes               bool
	flagOrchBackupDir         string
	flagOrchCopyTo            string
	flagOrchCopyPort          int
	flagOrchCopyRemoteDir     string
	flagOrchCopyRemoteService string
	flagOrchCopySudo          bool
	flagOrchCopyYes           bool
	flagOrchConfigShowSecrets bool
	flagOrchConfigRestart     bool
	flagOrchTemplateProvider  string
	flagOrchTemplateNetwork   string
	flagOrchTemplateBNB       string
	flagOrchTemplateBSC       string
	flagOrchTemplateETH       string
	flagOrchTemplateRestart   bool
)

var orchestratorCmd = &cobra.Command{
	Use:   "orchestrator",
	Short: "Deploy, configure and operate the Zenon orchestrator service",
	Long: `Deploy, upgrade, configure, back up and operate the Zenon orchestrator
systemd service (NOMCTL_ORCHESTRATOR_SERVICE, default "orchestrator") and its
state directory (NOMCTL_ORCHESTRATOR_DIR, default "/root/.orchestrator").`,
}

var orchestratorStatusCmd = &cobra.Command{
	Use:         "status",
	Short:       "Show whether the orchestrator is installed and running",
	Args:        cobra.NoArgs,
	Annotations: diagnostic(),
	RunE: func(cmd *cobra.Command, _ []string) error {
		ok, err := orchestrator.Installed(cfg)
		if err != nil {
			return err
		}
		if !ok {
			return orchestrator.ErrNotInstalled
		}
		st, err := service.Status(cfg.OrchestratorService)
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s: %s\n", cfg.OrchestratorService, st)
		return nil
	},
}

var orchestratorLogsCmd = &cobra.Command{
	Use:         "logs",
	Short:       "Show the orchestrator journal (use -f to follow)",
	Args:        cobra.NoArgs,
	Annotations: diagnostic(),
	RunE: func(*cobra.Command, []string) error {
		ok, err := orchestrator.Installed(cfg)
		if err != nil {
			return err
		}
		if !ok {
			return orchestrator.ErrNotInstalled
		}
		return tui.MonitorUnit(cfg.OrchestratorService, flagOrchLogsFollow, flagOrchLogsLines)
	},
}

var orchestratorInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Download a GitHub release, install /usr/local/bin/orchestrator, set up the service and start it",
	Long: `Deploys the Zenon orchestrator from a GitHub release. Defaults to
https://github.com/0x3639/orchestrator and its most recent published release.

Pass --repo with a GitHub account (e.g. HyperCore-Team), owner/repo, or full
GitHub URL to release from another account. The release archive's SHA-256 is
verified before stopping the service. Creates /etc/systemd/system/orchestrator.service
and a starter config.json when absent; existing service units and config files
are preserved.`,
	Args:        cobra.NoArgs,
	Annotations: rootOnly(),
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runOrchestratorDeploy(cmd, false)
	},
}

var orchestratorUpgradeCmd = &cobra.Command{
	Use:   "upgrade",
	Short: "Upgrade an installed orchestrator from a GitHub release",
	Long: `Upgrades an existing orchestrator installation from a GitHub release
(default: https://github.com/0x3639/orchestrator, most recent published release).

Downloads and verifies the release archive's SHA-256 before stopping the
orchestrator, preserves the previous binary as .previous, copies the new binary
to /usr/local/bin/orchestrator (NOMCTL_INSTALL_DIR/NOMCTL_ORCHESTRATOR_BINARY),
and restarts the service.`,
	Args:        cobra.NoArgs,
	Annotations: diagnostic(),
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runOrchestratorDeploy(cmd, true)
	},
}

func runOrchestratorDeploy(cmd *cobra.Command, upgradeOnly bool) error {
	interactive := ui.Interactive()
	if !interactive && !flagOrchYes {
		verb := "install"
		if upgradeOnly {
			verb = "upgrade"
		}
		return fmt.Errorf("non-interactive orchestrator %s requires --yes", verb)
	}
	if upgradeOnly {
		ok, err := orchestrator.Installed(cfg)
		if err != nil {
			return err
		}
		if !ok {
			return orchestrator.ErrNotInstalled
		}
	}
	repo := cfg.OrchestratorRepo
	if interactive && !flagOrchYes && !cmd.Flags().Changed("repo") {
		var err error
		repo, err = tui.PickOrchestratorRepo(cfg)
		if errors.Is(err, tui.ErrCancelled) {
			return errCancelled
		}
		if err != nil {
			return err
		}
	}
	client, err := orchestrator.NewClient(repo, cfg.GitHubToken)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	release, err := tui.ChooseOrchestratorRelease(ctx, client, flagOrchVersion, interactive && !flagOrchYes)
	if errors.Is(err, tui.ErrCancelled) {
		return errCancelled
	}
	if err != nil {
		return err
	}
	if !flagOrchYes {
		ok, err := tui.ConfirmOrchestratorDeploy(cfg, client.Repo(), release, upgradeOnly)
		if errors.Is(err, tui.ErrCancelled) {
			return errCancelled
		}
		if err != nil {
			return err
		}
		if !ok {
			return errCancelled
		}
	}
	op := "orchestrator install"
	if upgradeOnly {
		op = "orchestrator upgrade"
	}
	var res orchestrator.DeployResult
	if err := withLock(op, func() error {
		var err error
		if upgradeOnly {
			res, err = orchestrator.Upgrade(ctx, cfg, client, release)
		} else {
			res, err = orchestrator.Install(ctx, cfg, client, release)
		}
		return err
	}); err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Repository  %s\nRelease     %s\nBinary      %s\nSHA-256     %s\n",
		orchestrator.RepoURL(res.Repo), res.Tag, res.BinaryPath, res.BinarySHA256)
	if res.PreviousBinary != "" {
		fmt.Fprintf(out, "Previous    %s\n", res.PreviousBinary)
	}
	if res.UnitCreated {
		fmt.Fprintf(out, "Unit        %s (created and enabled)\n", res.UnitPath)
	}
	if res.ConfigCreated {
		fmt.Fprintf(out, "Config      starter config.json created under %s\n", cfg.OrchestratorDir)
	}
	return nil
}

func applyOrchestratorReleaseFlags(cmd *cobra.Command) error {
	if cmd.Flags().Changed("repo") {
		norm, err := orchestrator.NormalizeRepo(flagOrchRepo)
		if err != nil {
			return err
		}
		cfg.OrchestratorRepo = norm
	}
	return nil
}

var orchestratorBackupCmd = &cobra.Command{
	Use:   "backup",
	Short: "Back up orchestrator sync data (events/ and queues/) with a SHA-256 sidecar",
	Long: `Stops the orchestrator briefly to capture consistent LevelDB copies of
events/ and queues/, restarts the orchestrator before compression, and writes a
.tar.gz archive plus .hash sidecar under <NOMCTL_BACKUP_DIR>/orchestrator
(or --output). Configuration, producer keys, TSS shares and voting history are
excluded.`,
	Args:        cobra.NoArgs,
	Annotations: diagnostic(),
	RunE: func(cmd *cobra.Command, _ []string) error {
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		var res orchestrator.SyncBackupResult
		if err := withLock("orchestrator backup", func() error {
			var err error
			res, err = orchestrator.BackupSyncData(ctx, cfg, orchestrator.SyncBackupOptions{
				OutputDir: flagOrchBackupDir,
			})
			return err
		}); err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		fmt.Fprintf(out, "Archive   %s\nHash      %s\nSHA-256   %s\nIncluded  %s\n",
			res.ArchivePath, res.HashPath, res.SHA256, strings.Join(res.IncludedDirs, ", "))
		return nil
	},
}

var orchestratorCopySyncCmd = &cobra.Command{
	Use:   "copy-sync",
	Short: "Copy orchestrator sync data (events/ and queues/) to another server over SSH",
	Long: `Stops the local orchestrator, transfers events/ and queues/ with rsync over
SSH to a staging directory on the target server, stops the remote orchestrator
(if present), saves its existing sync data as a timestamped backup, moves the
new sync data into place, and restarts both active services.

Remote config.json, producer keys, TSS shares and voting history are untouched.`,
	Args:        cobra.NoArgs,
	Annotations: diagnostic(),
	RunE: func(cmd *cobra.Command, _ []string) error {
		opts := orchestrator.SyncCopyOptions{
			Host:          flagOrchCopyTo,
			Port:          flagOrchCopyPort,
			RemoteDir:     flagOrchCopyRemoteDir,
			RemoteService: flagOrchCopyRemoteService,
			Sudo:          flagOrchCopySudo,
			Progress:      cmd.ErrOrStderr(),
		}
		if err := opts.Validate(cfg); err != nil {
			return err
		}
		if !flagOrchCopyYes {
			if !ui.Interactive() {
				return errors.New("non-interactive orchestrator copy-sync requires --yes")
			}
			ok, err := tui.Confirm(fmt.Sprintf("Copy events/ and queues/ from %s to %s:%s?\n\nStops local %s, transfers via rsync over SSH, stops remote %s with a safety backup, installs the sync data, and restarts active services.",
				cfg.OrchestratorDir, opts.Host, opts.RemoteDir, cfg.OrchestratorService, opts.RemoteService))
			if errors.Is(err, tui.ErrCancelled) {
				return errCancelled
			}
			if err != nil {
				return err
			}
			if !ok {
				return errCancelled
			}
		}
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		var res orchestrator.SyncCopyResult
		if err := withLock("orchestrator copy-sync", func() error {
			var err error
			res, err = orchestrator.CopySyncData(ctx, cfg, opts)
			return err
		}); err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		fmt.Fprintf(out, "Destination    %s:%s\nCopied         %s\nRemote backup  %s\n",
			res.Host, res.RemoteDir, strings.Join(res.CopiedDirs, ", "), res.RemoteBackup)
		if res.RemoteUnitFound && !res.RemoteRestarted {
			fmt.Fprintf(out, "Remote service %s was stopped before copy and remains stopped.\n", res.RemoteService)
		}
		return nil
	},
}

func applyOrchestratorCopySyncFlags(_ *cobra.Command) error {
	opts := orchestrator.SyncCopyOptions{
		Host:          flagOrchCopyTo,
		Port:          flagOrchCopyPort,
		RemoteDir:     flagOrchCopyRemoteDir,
		RemoteService: flagOrchCopyRemoteService,
		Sudo:          flagOrchCopySudo,
	}
	return opts.Validate(cfg)
}

var orchestratorConfigCmd = &cobra.Command{
	Use:   "config",
	Short: "Show or edit the orchestrator's config.json with validation",
}

var orchestratorConfigShowCmd = &cobra.Command{
	Use:         "show",
	Short:       "Print orchestrator config.json (redacting secrets unless --show-secrets)",
	Args:        cobra.NoArgs,
	Annotations: diagnostic(),
	RunE: func(cmd *cobra.Command, _ []string) error {
		dir, err := orchestrator.ValidateDir(cfg.OrchestratorDir)
		if err != nil {
			return err
		}
		raw, err := orchestrator.ReadConfig(orchestrator.ConfigPath(dir))
		if err != nil {
			return err
		}
		if !flagOrchConfigShowSecrets {
			raw, err = orchestrator.RedactConfigJSON(raw)
			if err != nil {
				return err
			}
		}
		_, err = cmd.OutOrStdout().Write(raw)
		return err
	},
}

var orchestratorConfigEditCmd = &cobra.Command{
	Use:         "edit",
	Short:       "Open orchestrator config.json in $EDITOR; validates before atomic save",
	Args:        cobra.NoArgs,
	Annotations: diagnostic(),
	RunE: func(cmd *cobra.Command, _ []string) error {
		if !ui.Interactive() {
			return errors.New("orchestrator config edit needs a terminal")
		}
		dir, err := orchestrator.ValidateDir(cfg.OrchestratorDir)
		if err != nil {
			return err
		}
		return withLock("orchestrator config", func() error {
			outcome, backupPath, err := orchestrator.EditConfig(orchestrator.ConfigPath(dir), runEditor, tui.RetryEdit, time.Now())
			if err != nil {
				return err
			}
			switch outcome {
			case nodeconfig.EditUnchanged:
				fmt.Fprintln(cmd.OutOrStdout(), "No changes.")
				return nil
			case nodeconfig.EditAborted:
				fmt.Fprintln(cmd.OutOrStdout(), "Aborted; orchestrator config.json is unchanged.")
				return nil
			}
			logx.Success("orchestrator config.json written")
			if backupPath != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "Previous file kept as %s\n", backupPath)
			}
			return restartOrchestratorIfAsked(cmd.OutOrStdout(), flagOrchConfigRestart)
		})
	},
}

var orchestratorTemplateCmd = &cobra.Command{
	Use:   "template",
	Short: "List or apply Alchemy and dRPC (drpc.org) per-network sync templates",
}

var orchestratorTemplateListCmd = &cobra.Command{
	Use:         "list",
	Short:       "Show built-in Alchemy and dRPC sync templates per EVM network",
	Args:        cobra.NoArgs,
	Annotations: diagnostic(),
	RunE: func(cmd *cobra.Command, _ []string) error {
		out := cmd.OutOrStdout()
		fmt.Fprintf(out, "%-16s %-12s %-16s %-22s %s\n", "PROVIDER", "NETWORK", "FilterQuerySize", "RpcRequestsPerSecond", "RpcBurst")
		for _, t := range orchestrator.Templates() {
			fmt.Fprintf(out, "%-16s %-12s %-16d %-22g %d\n",
				t.DisplayName, t.Network, t.Settings.FilterQuerySize, t.Settings.RpcRequestsPerSecond, t.Settings.RpcBurst)
		}
		return nil
	},
}

var orchestratorTemplateApplyCmd = &cobra.Command{
	Use:   "apply",
	Short: "Apply Alchemy or dRPC sync settings to BNB Chain and/or Ethereum in config.json",
	Long: `Updates FilterQuerySize, RpcRequestsPerSecond and RpcBurst in the
orchestrator's config.json while preserving Urls and all other settings.

Apply one provider to a network or all networks:
  sudo nomctl orchestrator template apply --provider drpc --network all
  sudo nomctl orchestrator template apply --provider alchemy --network ethereum

Or assign different providers per network in one call:
  sudo nomctl orchestrator template apply --bnb drpc --eth alchemy --restart`,
	Args:        cobra.NoArgs,
	Annotations: diagnostic(),
	RunE: func(cmd *cobra.Command, _ []string) error {
		assignments, err := resolveTemplateAssignments(cmd)
		if err != nil {
			return err
		}
		dir, err := orchestrator.ValidateDir(cfg.OrchestratorDir)
		if err != nil {
			return err
		}
		return withLock("orchestrator template", func() error {
			res, err := orchestrator.ApplySyncTemplates(orchestrator.ConfigPath(dir), assignments, time.Now())
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			for _, tpl := range res.Applied {
				logx.Success(fmt.Sprintf("%s → %s (FilterQuerySize=%d, RpcRequestsPerSecond=%g, RpcBurst=%d)",
					tpl.Network, tpl.DisplayName, tpl.Settings.FilterQuerySize, tpl.Settings.RpcRequestsPerSecond, tpl.Settings.RpcBurst))
			}
			if res.BackupPath != "" {
				fmt.Fprintf(out, "Previous file kept as %s\n", res.BackupPath)
			}
			return restartOrchestratorIfAsked(out, flagOrchTemplateRestart)
		})
	},
}

func resolveTemplateAssignments(cmd *cobra.Command) (map[string]string, error) {
	assignments := map[string]string{}
	hasProvider := cmd.Flags().Changed("provider") && strings.TrimSpace(flagOrchTemplateProvider) != ""
	hasBNB := cmd.Flags().Changed("bnb") && strings.TrimSpace(flagOrchTemplateBNB) != ""
	hasBSC := cmd.Flags().Changed("bsc") && strings.TrimSpace(flagOrchTemplateBSC) != ""
	hasETH := cmd.Flags().Changed("eth") && strings.TrimSpace(flagOrchTemplateETH) != ""

	if hasBNB && hasBSC {
		return nil, errors.New("specify either --bnb or --bsc, not both")
	}
	if hasProvider {
		prov, err := orchestrator.NormalizeProvider(flagOrchTemplateProvider)
		if err != nil {
			return nil, err
		}
		netArg := strings.ToLower(strings.TrimSpace(flagOrchTemplateNetwork))
		if netArg == "" || netArg == "all" {
			assignments[orchestrator.NetworkBNBChain] = prov
			assignments[orchestrator.NetworkEthereum] = prov
		} else {
			normNet, err := orchestrator.NormalizeNetwork(netArg)
			if err != nil {
				return nil, err
			}
			assignments[normNet] = prov
		}
	} else if cmd.Flags().Changed("network") && !hasBNB && !hasBSC && !hasETH {
		return nil, errors.New("--network requires --provider (alchemy or drpc)")
	}
	if hasBNB {
		prov, err := orchestrator.NormalizeProvider(flagOrchTemplateBNB)
		if err != nil {
			return nil, err
		}
		assignments[orchestrator.NetworkBNBChain] = prov
	}
	if hasBSC {
		prov, err := orchestrator.NormalizeProvider(flagOrchTemplateBSC)
		if err != nil {
			return nil, err
		}
		assignments[orchestrator.NetworkBNBChain] = prov
	}
	if hasETH {
		prov, err := orchestrator.NormalizeProvider(flagOrchTemplateETH)
		if err != nil {
			return nil, err
		}
		assignments[orchestrator.NetworkEthereum] = prov
	}
	if len(assignments) == 0 {
		return nil, errors.New("specify --provider (alchemy|drpc) or per-network flags (--bnb/--eth)")
	}
	return assignments, nil
}

func applyOrchestratorTemplateFlags(cmd *cobra.Command) error {
	_, err := resolveTemplateAssignments(cmd)
	return err
}

func restartOrchestratorIfAsked(out io.Writer, restart bool) error {
	if !restart {
		if service.IsActive(cfg.OrchestratorService) {
			fmt.Fprintf(out, "%s reads config.json at start: sudo systemctl restart %s (or pass --restart)\n", cfg.OrchestratorService, cfg.OrchestratorService)
		}
		return nil
	}
	return service.Restart(cfg.OrchestratorService)
}

var orchestratorRecoverCmd = &cobra.Command{
	Use:   "recover",
	Short: "Complete a pending orchestrator rollback or .previous binary publication",
	Long: `Finishes an interrupted or unresolved orchestrator install/upgrade recorded
in <binary>.pending.json: stops the original unit, restores the previous binary
and restarts it (for a pending rollback), or finishes promoting the retained
previous binary to .previous without restarting a verified running release.`,
	Args:        cobra.NoArgs,
	Annotations: diagnostic(),
	RunE: func(cmd *cobra.Command, _ []string) error {
		var summary string
		if err := withLock("orchestrator recover", func() error {
			var err error
			summary, err = orchestrator.RecoverPendingDeploy(cfg)
			return err
		}); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), summary)
		return nil
	},
}

var orchestratorHardResetCmd = &cobra.Command{
	Use:   "hard-reset",
	Short: "Stop the orchestrator, delete its queues and events, start it again",
	Long: `Ports the orchestrator hard-reset script: stops the orchestrator unit,
waits ten seconds, deletes queues/ and events/ under the orchestrator
directory (NOMCTL_ORCHESTRATOR_DIR, default /root/.orchestrator) and starts
the unit again. The orchestrator rebuilds both from the chain.

This command does not ask for confirmation; the interactive menu does.`,
	Args:        cobra.NoArgs,
	Annotations: rootOnly(),
	RunE: func(*cobra.Command, []string) error {
		return withLock("orchestrator hard-reset", func() error { return orchestrator.HardReset(cfg) })
	},
}

func init() {
	orchestratorLogsCmd.Flags().BoolVarP(&flagOrchLogsFollow, "follow", "f", false, "follow the journal in real time")
	orchestratorLogsCmd.Flags().IntVarP(&flagOrchLogsLines, "lines", "n", 20, "number of recent lines to show when not following")

	for _, c := range []*cobra.Command{orchestratorInstallCmd, orchestratorUpgradeCmd} {
		c.Flags().StringVar(&flagOrchRepo, "repo", "", "GitHub account, owner/repo, or URL (default: https://github.com/0x3639/orchestrator)")
		c.Flags().StringVar(&flagOrchVersion, "version", "", "release tag to install (default: most recent published release)")
		c.Flags().BoolVarP(&flagOrchYes, "yes", "y", false, "skip prompts and use defaults")
		applyFlags[c] = applyOrchestratorReleaseFlags
	}

	orchestratorBackupCmd.Flags().StringVar(&flagOrchBackupDir, "output", "", "destination directory for sync backup archives (default: <NOMCTL_BACKUP_DIR>/orchestrator)")

	orchestratorCopySyncCmd.Flags().StringVar(&flagOrchCopyTo, "to", "", "destination SSH host, such as root@other-server")
	orchestratorCopySyncCmd.Flags().IntVar(&flagOrchCopyPort, "port", 22, "SSH port")
	orchestratorCopySyncCmd.Flags().StringVar(&flagOrchCopyRemoteDir, "remote-dir", "", "remote orchestrator directory (default: NOMCTL_ORCHESTRATOR_DIR)")
	orchestratorCopySyncCmd.Flags().StringVar(&flagOrchCopyRemoteService, "remote-service", "", "remote orchestrator systemd unit name (default: NOMCTL_ORCHESTRATOR_SERVICE)")
	orchestratorCopySyncCmd.Flags().BoolVar(&flagOrchCopySudo, "sudo", false, "use noninteractive sudo on the remote server")
	orchestratorCopySyncCmd.Flags().BoolVarP(&flagOrchCopyYes, "yes", "y", false, "skip confirmation prompt")
	applyFlags[orchestratorCopySyncCmd] = applyOrchestratorCopySyncFlags

	orchestratorConfigShowCmd.Flags().BoolVar(&flagOrchConfigShowSecrets, "show-secrets", false, "print unredacted passphrase and RPC URLs")
	orchestratorConfigEditCmd.Flags().BoolVar(&flagOrchConfigRestart, "restart", false, "restart the orchestrator after saving")
	orchestratorConfigCmd.AddCommand(orchestratorConfigShowCmd, orchestratorConfigEditCmd)

	orchestratorTemplateApplyCmd.Flags().StringVar(&flagOrchTemplateProvider, "provider", "", "sync provider template (alchemy or drpc)")
	orchestratorTemplateApplyCmd.Flags().StringVar(&flagOrchTemplateNetwork, "network", "all", "target network for --provider (bnb, ethereum, or all)")
	orchestratorTemplateApplyCmd.Flags().StringVar(&flagOrchTemplateBNB, "bnb", "", "sync provider template for BNB Chain (alchemy or drpc)")
	orchestratorTemplateApplyCmd.Flags().StringVar(&flagOrchTemplateBSC, "bsc", "", "alias for --bnb (targets BNB Chain)")
	orchestratorTemplateApplyCmd.Flags().StringVar(&flagOrchTemplateETH, "eth", "", "sync provider template for Ethereum (alchemy or drpc)")
	orchestratorTemplateApplyCmd.Flags().BoolVar(&flagOrchTemplateRestart, "restart", false, "restart the orchestrator after applying templates")
	applyFlags[orchestratorTemplateApplyCmd] = applyOrchestratorTemplateFlags
	orchestratorTemplateCmd.AddCommand(orchestratorTemplateListCmd, orchestratorTemplateApplyCmd)

	orchestratorCmd.AddCommand(
		orchestratorInstallCmd,
		orchestratorUpgradeCmd,
		orchestratorRecoverCmd,
		orchestratorStatusCmd,
		orchestratorLogsCmd,
		orchestratorBackupCmd,
		orchestratorCopySyncCmd,
		orchestratorConfigCmd,
		orchestratorTemplateCmd,
		orchestratorHardResetCmd,
	)
	rootCmd.AddCommand(orchestratorCmd)
}
