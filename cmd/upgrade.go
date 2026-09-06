package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/0x3639/nomctl/internal/alerts"
	"github.com/0x3639/nomctl/internal/logx"
	"github.com/0x3639/nomctl/internal/service"
	"github.com/0x3639/nomctl/internal/update"
)

var (
	flagUpgradeCheck    bool
	flagUpgradeVersion  string
	flagUpgradeRollback bool
	flagNoUpdateCheck   bool
)

var upgradeCmd = &cobra.Command{
	Use:   "upgrade",
	Short: "Upgrade nomctl to the latest release",
	Long: `Downloads the latest GitHub release for this architecture, verifies it
against checksums.txt, replaces the running binary atomically and keeps the
previous one for --rollback. Restarts nomctl-alerts.service if it is running
so the daemon uses the new code.`,
	Args:        cobra.NoArgs,
	Annotations: diagnostic(),
	RunE: func(cmd *cobra.Command, _ []string) error {
		out := cmd.OutOrStdout()
		target, err := os.Executable()
		if err != nil {
			return err
		}
		if resolved, err := filepath.EvalSymlinks(target); err == nil {
			target = resolved
		}
		if flagUpgradeRollback && (flagUpgradeCheck || flagUpgradeVersion != "") {
			return errors.New("--rollback cannot be combined with --check or --version")
		}
		if flagUpgradeRollback {
			if err := update.Rollback(target); err != nil {
				return err
			}
			logx.Success("Rolled back to the previous nomctl binary")
			return restartAlertsIfRunning()
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		tag := strings.TrimSpace(flagUpgradeVersion)
		if tag == "" {
			tag, err = update.LatestTag(ctx, cfg.ReleaseRepo)
			if err != nil {
				return fmt.Errorf("check latest release: %w", err)
			}
		} else if !strings.HasPrefix(tag, "v") {
			tag = "v" + tag
		}
		fmt.Fprintf(out, "Running %s, latest %s\n", version, strings.TrimPrefix(tag, "v"))
		if !update.Newer(tag, version) && flagUpgradeVersion == "" {
			if _, ok := update.ParseVersion(version); !ok {
				fmt.Fprintln(out, "This is a development build; pass --version to install a release anyway.")
				return nil
			}
			fmt.Fprintln(out, "Already up to date.")
			return nil
		}
		if flagUpgradeCheck {
			fmt.Fprintf(out, "Update available: https://github.com/%s/releases/tag/%s\n", cfg.ReleaseRepo, tag)
			return nil
		}

		dir, err := os.MkdirTemp(filepath.Dir(target), ".nomctl-upgrade-")
		if err != nil {
			return err
		}
		defer func() { _ = os.RemoveAll(dir) }()
		bin, err := update.Fetch(ctx, cfg.ReleaseRepo, tag, dir)
		if err != nil {
			return err
		}
		if err := update.Replace(bin, target); err != nil {
			return err
		}
		logx.Success(fmt.Sprintf("nomctl %s installed at %s (previous kept as %s.previous)", strings.TrimPrefix(tag, "v"), target, filepath.Base(target)))
		fmt.Fprintf(out, "Release notes: https://github.com/%s/releases/tag/%s\n", cfg.ReleaseRepo, tag)
		return restartAlertsIfRunning()
	},
}

// restartAlertsIfRunning restarts the alerts daemon so it runs the new code.
func restartAlertsIfRunning() error {
	if !service.IsActive(alerts.UnitName) {
		return nil
	}
	if err := service.RestartUnit(alerts.UnitName + ".service"); err != nil {
		return errors.New("nomctl was upgraded but " + alerts.UnitName + " could not be restarted: " + err.Error())
	}
	logx.Success(alerts.UnitName + ".service restarted")
	return nil
}

func init() {
	upgradeCmd.Flags().BoolVar(&flagUpgradeCheck, "check", false, "only report whether an update is available")
	upgradeCmd.Flags().StringVar(&flagUpgradeVersion, "version", "", "install this release tag instead of the latest")
	upgradeCmd.Flags().BoolVar(&flagUpgradeRollback, "rollback", false, "restore the previous binary")
	rootCmd.AddCommand(upgradeCmd)
}
