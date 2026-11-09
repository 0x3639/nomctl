package cmd

import (
	"fmt"
	"github.com/0x3639/nomctl/internal/backup"
	"github.com/0x3639/nomctl/internal/restore"
	"github.com/0x3639/nomctl/internal/tui"
	"github.com/0x3639/nomctl/internal/ui"
	"github.com/spf13/cobra"
)

var flagPointKeep, flagPointLabel string
var flagPointDataOnly bool

var backupPointCmd = &cobra.Command{
	Use: "point", Short: "Back up chain data and znnd with its version and hashes into a protected restore point",
	Args: cobra.NoArgs, Annotations: rootOnly(),
	RunE: func(command *cobra.Command, _ []string) error {
		if ui.Interactive() && !command.Flags().Changed("keep") {
			if err := tui.ChooseRestorePointKeep(&cfg); err != nil {
				return err
			}
		}
		return withLock("create restore point", func() error {
			path, err := backup.RunRestorePoint(cfg, flagPointLabel)
			if err == nil {
				fmt.Fprintln(command.OutOrStdout(), path)
			}
			return err
		})
	},
}

var restorePointCmd = &cobra.Command{
	Use: "point ID", Short: "Restore chain data and optionally the saved znnd from a restore point",
	Long: "Restore a protected restore point. Interactive use asks whether to also restore\nthe saved znnd. Non-interactive use restores the matching data and znnd by default;\n--data-only keeps the installed znnd, which must support the saved chain data.",
	Args: cobra.ExactArgs(1), Annotations: rootOnly(),
	RunE: func(_ *cobra.Command, args []string) error {
		if ui.Interactive() && !flagPointDataOnly {
			return tui.RestorePointByID(cfg, args[0])
		}
		return withLock("restore from restore point", func() error {
			return restore.RunRestorePointWithOptions(cfg, args[0], restore.RestorePointOptions{DataOnly: flagPointDataOnly})
		})
	},
}

var restorePointsCmd = &cobra.Command{
	Use: "restore-points", Short: "Manage protected restore points in the UI; list them in non-interactive mode",
	Args: cobra.NoArgs, Annotations: diagnostic(),
	RunE: func(command *cobra.Command, _ []string) error {
		if ui.Interactive() {
			return tui.ManageRestorePoints(cfg)
		}
		points, err := backup.ListRestorePoints(cfg)
		if err != nil {
			return err
		}
		for _, p := range points {
			fmt.Fprintf(command.OutOrStdout(), "%s\t%.1f MiB\tkeep-days=%d (0=indefinite)\tcomplete=%t\t%s\n%s\n", p.ID, float64(p.Size)/(1<<20), p.KeepDays, p.Complete, p.Label, p.BinaryDescription())
		}
		return nil
	},
}

var manageBackupsCmd = &cobra.Command{
	Use: "manage", Short: "Review and prune ordinary local backups in the UI",
	Args: cobra.NoArgs, Annotations: diagnostic(),
	RunE: func(*cobra.Command, []string) error {
		if !ui.Interactive() {
			return fmt.Errorf("backup management requires the interactive UI")
		}
		return tui.ManageBackups(cfg)
	},
}

func init() {
	backupPointCmd.Flags().StringVar(&flagPointKeep, "keep", "indefinite", "1-30 days or indefinite; deletion is always manual in the UI")
	backupPointCmd.Flags().StringVar(&flagPointLabel, "label", "Manual restore point", "description of the restore point")
	applyFlags[backupPointCmd] = func(command *cobra.Command) error {
		if command.Flags().Changed("keep") {
			days, err := backup.ParseRestorePointKeep(flagPointKeep)
			if err != nil {
				return err
			}
			cfg.RestorePointKeepDays = days
		}
		return cfg.Validate()
	}
	backupCmd.AddCommand(backupPointCmd)
	backupCmd.AddCommand(manageBackupsCmd)

	restorePointCmd.Flags().BoolVar(&flagPointDataOnly, "data-only", false, "restore chain data only; keep the installed znnd unchanged")
	restoreCmd.AddCommand(restorePointCmd)
	rootCmd.AddCommand(restorePointsCmd)
}
