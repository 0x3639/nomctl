package cmd

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/0x3639/nomctl/internal/backup"
	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/restore"
	"github.com/0x3639/nomctl/internal/resync"
	"github.com/0x3639/nomctl/internal/tui"
	"github.com/0x3639/nomctl/internal/ui"
)

var (
	flagBackupMax       int
	flagBackupRetention int
	flagBackupCadence   int
	flagBackupHour      int
	flagBackupSchedule  bool
	flagRestoreFile     string
)

var backupCmd = &cobra.Command{
	Use:   "backup",
	Short: "Snapshot the node data directory into the backup directory",
	Long: `Stops the node, copies the chain data, restarts the node, then archives the
copy as <service>_backup_<timestamp>.tar.gz with a sha256 sidecar and prunes
archives older than --retention-days (default 7, maximum 30) or beyond
--max-backups. Old chain backups are pruned when necessary to keep total
filesystem usage at or below 75%, including staging and archive space.

With --cadence N, a manual backup is skipped when the newest archive is
younger than N days. With --schedule, a daily cron job is installed after
this backup. Use "nomctl backup schedule" to install the job without
running a backup now. Daily cron runs do not use the manual cadence.`,
	Args:        cobra.NoArgs,
	Annotations: rootOnly(),
	RunE: func(*cobra.Command, []string) error {
		return withLock("backup", func() error {
			if _, err := backup.Run(cfg, backup.Options{Interactive: false}); err != nil {
				return err
			}
			if flagBackupSchedule {
				return backup.LocalSchedule(cfg)
			}
			return nil
		})
	},
}

var backupScheduleCmd = &cobra.Command{
	Use:         "schedule",
	Short:       "Schedule daily local backups with cron (no backup runs now)",
	Args:        cobra.NoArgs,
	Annotations: rootOnly(),
	RunE: func(*cobra.Command, []string) error {
		return withLock("schedule local backups", func() error { return backup.LocalSchedule(cfg) })
	},
}

var restoreCmd = &cobra.Command{
	Use:   "restore",
	Short: "Restore chain data from a backup archive; keep the installed znnd unchanged",
	Long: `Restores chain data only; the installed znnd and its version stay unchanged.
Verifies the archive against its sha256 sidecar, stops the node, moves the
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
		return withLock("restore", func() error { return restore.Run(cfg, file) })
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
	RunE: func(*cobra.Command, []string) error {
		return withLock("resync", func() error { return resync.Run(cfg) })
	},
}

// applyBackupFlags copies backup flags over the environment and validates.
func applyBackupFlags(cmd *cobra.Command) error {
	if cmd.Flags().Changed("retention-days") {
		cfg.BackupRetentionDays = flagBackupRetention
	}
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
	return cfg.Validate()
}

func init() {
	for _, command := range []*cobra.Command{backupCmd, backupScheduleCmd} {
		applyFlags[command] = applyBackupFlags
		command.Flags().IntVar(&flagBackupRetention, "retention-days", config.DefaultBackupRetentionDays, "days to keep local backups, 1-30 (NOMCTL_BACKUP_RETENTION_DAYS)")
		command.Flags().IntVar(&flagBackupHour, "hour", -1, "hour of day 0-23 for the scheduled backup (NOMCTL_BACKUP_HOUR)")
	}
	backupCmd.Flags().IntVar(&flagBackupMax, "max-backups", 0, "backups to keep, 1-30 (NOMCTL_MAX_BACKUPS)")
	backupCmd.Flags().IntVar(&flagBackupCadence, "cadence", 0, "minimum age of newest backup before running, 0-365 days (NOMCTL_BACKUP_CADENCE_DAYS)")
	backupCmd.AddCommand(backupScheduleCmd)
	backupCmd.Flags().BoolVar(&flagBackupSchedule, "schedule", false, "install a daily local-backup cron job after this backup")
	restoreCmd.Flags().StringVar(&flagRestoreFile, "file", "", "archive to restore (path or name in the backup directory)")
	rootCmd.AddCommand(backupCmd, restoreCmd, resyncCmd)
}
