package cmd

import (
	"errors"
	"fmt"
	"strings"

	"github.com/0x3639/nomctl/internal/backup"
	"github.com/0x3639/nomctl/internal/nodeupgrade"
	"github.com/0x3639/nomctl/internal/tui"
	"github.com/0x3639/nomctl/internal/ui"
	"github.com/spf13/cobra"
)

var (
	flagNodeUpgradeKeep    string
	flagNodeUpgradeVersion string
	flagNodeUpgradeYes     bool
	flagNodeUpgradeSigned  bool
	flagNodeUpgradeRepo    string
	flagNodeUpgradeBranch  string
)

var upgradeNodeCmd = &cobra.Command{
	Use:   "upgrade-node",
	Short: "Upgrade a node from source, or use --signed for a release",
	Long: `By default, build the configured repository and branch while the node runs,
then create a protected restore point, install, start, and finish its archive. --repo and
--branch override NOMCTL_REPO_URL and NOMCTL_BRANCH_NAME. Requires an existing
node installation and preserves its service unit.

Use --signed to select a published release from zenon-network/go-zenon-releases
(newest first). Its GPG signature and SHA-256 are verified before stopping the
node. The same restore-point, installation, restart and archive sequence follows.
--version requires --signed and selects an exact published release tag.
--repo and --branch cannot be combined with --signed.

Before either upgrade, a restore point is required. nomctl creates it automatically
to back up the current chain data and installed znnd so you can recover if needed.
It records the znnd version and commit when available, plus its SHA-256 checksum.
Both paths check space before downtime. Wallet and config use a separate encrypted
backup.

Choose --restore-point-keep indefinite (default) to keep the point until you
delete it individually, or 1-30 days to make it eligible for manual pruning after
that time. Both actions are in Manage restore points; nothing is auto-deleted.

Signed releases require GnuPG (gpg) and NOMCTL_GITHUB_TOKEN with Contents read
access while the repository is private. --yes skips prompts, using the configured
source repository/branch by default or the newest published release with --signed.
--yes does not skip the required restore point.`,
	Args:        cobra.NoArgs,
	Annotations: rootOnly(),
	RunE: func(cmd *cobra.Command, _ []string) error {
		interactive := ui.Interactive()
		if !interactive && !flagNodeUpgradeYes {
			return fmt.Errorf("non-interactive node upgrades require --yes")
		}
		if interactive && !flagNodeUpgradeYes && !cmd.Flags().Changed("restore-point-keep") {
			if err := tui.ChooseUpgradeRestorePointKeep(&cfg); err != nil {
				return err
			}
		}
		if !flagNodeUpgradeSigned {
			if !flagNodeUpgradeYes {
				ok, err := tui.ConfirmSourceUpgrade(cfg)
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
			return withLock("upgrade node from source", func() error {
				return nodeupgrade.FromSource(cmd.Context(), cfg, cfg.RepoURL, cfg.BranchName)
			})
		}
		client := nodeupgrade.NewClient(cfg.GitHubToken)
		release, err := tui.ChooseNodeRelease(cmd.Context(), client, flagNodeUpgradeVersion, interactive && !flagNodeUpgradeYes)
		if errors.Is(err, tui.ErrCancelled) {
			return errCancelled
		}
		if err != nil {
			return err
		}
		if !flagNodeUpgradeYes {
			ok, err := tui.ConfirmNodeUpgrade(cfg, release)
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
		return withLock("upgrade node", func() error { return nodeupgrade.Run(cmd.Context(), cfg, client, release) })
	},
}

func init() {
	upgradeNodeCmd.Flags().StringVar(&flagNodeUpgradeKeep, "restore-point-keep", "indefinite", "restore point retention: 1-30 days or indefinite; deletion is always manual in the UI")
	upgradeNodeCmd.Flags().StringVar(&flagNodeUpgradeVersion, "version", "", "install this published release tag (requires --signed)")
	upgradeNodeCmd.Flags().BoolVarP(&flagNodeUpgradeYes, "yes", "y", false, "skip prompts; use configured source, or newest release with --signed")
	upgradeNodeCmd.Flags().BoolVar(&flagNodeUpgradeSigned, "signed", false, "upgrade from a signed release instead of source")
	upgradeNodeCmd.Flags().StringVar(&flagNodeUpgradeRepo, "repo", "", "source repository URL (incompatible with --signed)")
	upgradeNodeCmd.Flags().StringVar(&flagNodeUpgradeBranch, "branch", "", "source branch (incompatible with --signed)")
	applyFlags[upgradeNodeCmd] = applyNodeUpgradeFlags
	rootCmd.AddCommand(upgradeNodeCmd)
}

func applyNodeUpgradeFlags(cmd *cobra.Command) error {
	if cmd.Flags().Changed("restore-point-keep") {
		days, err := backup.ParseRestorePointKeep(flagNodeUpgradeKeep)
		if err != nil {
			return err
		}
		cfg.RestorePointKeepDays = days
	}
	if !flagNodeUpgradeSigned && cmd.Flags().Changed("version") {
		return fmt.Errorf("--version requires --signed")
	}
	if flagNodeUpgradeSigned && (cmd.Flags().Changed("repo") || cmd.Flags().Changed("branch")) {
		return fmt.Errorf("--repo and --branch cannot be combined with --signed")
	}
	if cmd.Flags().Changed("repo") {
		if strings.TrimSpace(flagNodeUpgradeRepo) == "" {
			return fmt.Errorf("--repo cannot be empty")
		}
		cfg.RepoURL = strings.TrimSpace(flagNodeUpgradeRepo)
	}
	if cmd.Flags().Changed("branch") {
		if strings.TrimSpace(flagNodeUpgradeBranch) == "" {
			return fmt.Errorf("--branch cannot be empty")
		}
		cfg.BranchName = strings.TrimSpace(flagNodeUpgradeBranch)
	}
	return cfg.Validate()
}
