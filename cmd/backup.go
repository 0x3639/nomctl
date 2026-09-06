package cmd

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/0x3639/nomctl/internal/backup"
	"github.com/0x3639/nomctl/internal/restore"
	"github.com/0x3639/nomctl/internal/resync"
	"github.com/0x3639/nomctl/internal/tui"
	"github.com/0x3639/nomctl/internal/ui"
)

var (
	flagBackupMax      int
	flagBackupCadence  int
	flagBackupHour     int
	flagBackupSchedule bool
	flagRestoreFile    string
)

var backupCmd = &cobra.Command{
	Use:   "backup",
	Short: "Snapshot the node data directory into the backup directory",
	Long: `Stops the node, copies the chain data, restarts the node, then archives the
copy as <service>_backup_<timestamp>.tar.gz with a sha256 sidecar and prunes
archives beyond --max-backups.

With --cadence N, the backup is skipped when the newest archive is younger
than N days (useful from the scheduled timer). With --schedule, a systemd
timer is installed that runs "nomctl backup" daily at --hour (or a
deterministic time between 02:00 and 04:59).`,
	Args:        cobra.NoArgs,
	Annotations: rootOnly(),
	RunE: func(cmd *cobra.Command, _ []string) error {
		if cmd.Flags().Changed("max-backups") {
			cfg.MaxBackups = flagBackupMax
		}
		if cmd.Flags().Changed("cadence") {
			cfg.BackupCadenceDays = flagBackupCadence
		}
		if cmd.Flags().Changed("hour") {
			cfg.BackupHour = flagBackupHour
		}
		if cfg.MaxBackups > 30 {
			return fmt.Errorf("max backups must be between 1 and 30 (got %d)", cfg.MaxBackups)
		}
		if cfg.BackupCadenceDays > 365 {
			return fmt.Errorf("cadence must be between 0 and 365 days (got %d)", cfg.BackupCadenceDays)
		}
		if err := cfg.Validate(); err != nil {
			return err
		}
		if _, err := backup.Run(cfg, backup.Options{Interactive: false}); err != nil {
			return err
		}
		if flagBackupSchedule {
			return backup.Schedule(cfg)
		}
		return nil
	},
}

var restoreCmd = &cobra.Command{
	Use:   "restore",
	Short: "Restore the node data directory from a backup archive",
	Long: `Verifies the archive against its sha256 sidecar, stops the node, moves the
current chain data into <backup dir>/restore as a safety snapshot, extracts
the archive and starts the node again.

--file accepts a path or a bare archive name inside the backup directory.
When omitted on a terminal, an interactive picker is shown.`,
	Args:        cobra.NoArgs,
	Annotations: rootOnly(),
	RunE: func(cmd *cobra.Command, _ []string) error {
		file := flagRestoreFile
		if file == "" {
			if !ui.Interactive() {
				return errors.New("--file <archive> is required in non-interactive mode")
			}
			chosen, err := tui.PickBackup(cfg)
			if err != nil {
				return err
			}
			file = chosen
		} else {
			file = restore.Resolve(cfg, file)
		}
		return restore.Run(cfg, file)
	},
}

var resyncCmd = &cobra.Command{
	Use:   "resync",
	Short: "Wipe chain data and resync from genesis (keeps wallet and config)",
	Long: `Stops the node if running, deletes the network, nom, consensus and log
directories under the data directory, then starts the node again if it was
running. The wallet and config.json are preserved.

This command does not ask for confirmation; the interactive menu does.`,
	Args:        cobra.NoArgs,
	Annotations: rootOnly(),
	RunE:        func(*cobra.Command, []string) error { return resync.Run(cfg) },
}

func init() {
	backupCmd.Flags().IntVar(&flagBackupMax, "max-backups", 0, "backups to keep, 1-30 (NOMCTL_MAX_BACKUPS)")
	backupCmd.Flags().IntVar(&flagBackupCadence, "cadence", 0, "days between scheduled backups, 0-365 (NOMCTL_BACKUP_CADENCE_DAYS)")
	backupCmd.Flags().IntVar(&flagBackupHour, "hour", -1, "hour of day 0-23 for the scheduled backup (NOMCTL_BACKUP_HOUR)")
	backupCmd.Flags().BoolVar(&flagBackupSchedule, "schedule", false, "install a systemd timer that runs the backup daily")
	restoreCmd.Flags().StringVar(&flagRestoreFile, "file", "", "archive to restore (path or name in the backup directory)")
	rootCmd.AddCommand(backupCmd, restoreCmd, resyncCmd)
}
