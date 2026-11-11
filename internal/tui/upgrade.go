package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/nodeupgrade"
	"github.com/charmbracelet/huh"
)

// PickNodeRelease defaults to the newest published release (the first entry).
func PickNodeRelease(releases []nodeupgrade.Release) (string, error) {
	if len(releases) == 0 {
		return "", fmt.Errorf("no published releases available")
	}
	options := make([]huh.Option[string], 0, len(releases))
	for i, r := range releases {
		label := fmt.Sprintf("%s · %s", r.Tag, r.PublishedAt.Format("2006-01-02"))
		if r.Prerelease {
			label += " (prerelease)"
		}
		if i == 0 {
			label += " (most recent)"
		}
		options = append(options, huh.NewOption(label, r.Tag))
	}
	chosen := releases[0].Tag
	sel := huh.NewSelect[string]().Title("SELECT GO-ZENON RELEASE:").Options(options...).Value(&chosen).Height(12)
	if err := runForm(sel); err != nil {
		return "", err
	}
	return chosen, nil
}

// ChooseNodeRelease is shared by the CLI and main-menu action.
func ChooseNodeRelease(ctx context.Context, client *nodeupgrade.Client, tag string, pick bool) (nodeupgrade.Release, error) {
	releases, err := client.List(ctx)
	if err != nil {
		return nodeupgrade.Release{}, err
	}
	tag = strings.TrimSpace(tag)
	if tag == "" && pick {
		tag, err = PickNodeRelease(releases)
		if err != nil {
			return nodeupgrade.Release{}, err
		}
	}
	return nodeupgrade.Select(releases, tag)
}

func ConfirmNodeUpgrade(cfg config.Config, release nodeupgrade.Release) (bool, error) {
	return Confirm(fmt.Sprintf("Upgrade %s to %s from go-zenon-releases?\n\n%s\n\nVerify download, stop %s, save the restore-point data, install at %s,\nstart service, then finish the restore-point archive.", cfg.BinaryName, release.Tag, upgradeRestorePointSummary(cfg), cfg.ServiceName, cfg.BinaryPath()))
}

func UpgradeNode(cfg config.Config) error {
	ctx := context.Background()
	client := nodeupgrade.NewClient(cfg.GitHubToken)
	release, err := ChooseNodeRelease(ctx, client, "", true)
	if err != nil {
		return err
	}
	if err := ChooseUpgradeRestorePointKeep(&cfg); err != nil {
		return err
	}
	ok, err := ConfirmNodeUpgrade(cfg, release)
	if err != nil {
		return err
	}
	if !ok {
		return ErrCancelled
	}
	return withLock("upgrade node", func() error { return nodeupgrade.Run(ctx, cfg, client, release) })
}

// ConfirmSourceUpgrade describes downtime and the mandatory safety backup.
func ConfirmSourceUpgrade(cfg config.Config) (bool, error) {
	return Confirm(fmt.Sprintf("Upgrade a node from source?\n\nRepository: %s\nBranch: %s\n\n%s\n\nBuild while the node runs, then stop %s, save the restore-point data,\ninstall at %s, start, and finish the restore-point archive.", cfg.RepoURL, cfg.BranchName, upgradeRestorePointSummary(cfg), cfg.ServiceName, cfg.BinaryPath()))
}

func upgradeRestorePointSummary(cfg config.Config) string {
	keep := "Keep indefinitely; delete individually in Manage restore points."
	if cfg.RestorePointKeepDays > 0 {
		keep = fmt.Sprintf("Eligible for manual pruning after %d day(s) in Manage restore points; never auto-deleted.", cfg.RestorePointKeepDays)
	}
	return "A restore point is required: nomctl backs up current chain data and znnd before installing.\n" +
		keep + "\nWallet and config use a separate encrypted backup."
}

func UpgradeSource(cfg config.Config) error {
	if err := setupPreflight(cfg); err != nil {
		return err
	}
	repo, branch, err := PickSource(cfg)
	if err != nil {
		return err
	}
	cfg.RepoURL, cfg.BranchName = repo, branch
	if err := ChooseUpgradeRestorePointKeep(&cfg); err != nil {
		return err
	}
	ok, err := ConfirmSourceUpgrade(cfg)
	if err != nil {
		return err
	}
	if !ok {
		return ErrCancelled
	}
	return withLock("upgrade node from source", func() error { return nodeupgrade.FromSource(context.Background(), cfg, repo, branch) })
}
