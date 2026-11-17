package cmd

import (
	"encoding/json"
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
	flagBackupScheduled bool
	flagBackupJSON      bool
	flagBackupDirectory string
	flagEditHour        int
	flagEditRetention   int
	flagEditDirectory   string
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
running a backup now. Scheduling installs cron with apt if it is missing
on Debian/Ubuntu, then enables and checks the cron service. Daily cron
runs do not use the manual cadence.`,
	Args:        cobra.NoArgs,
	Annotations: rootOnly(),
	RunE: func(*cobra.Command, []string) error {
		if flagBackupScheduled {
			if flagBackupSchedule {
				return errors.New("--scheduled cannot be combined with --schedule")
			}
			return backup.RunScheduled(operationLockPath)
		}
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
	Use:   "schedule",
	Short: "Set up or manage daily local backups (no backup runs now)",
	Long: `Schedule one daily local-backup cron job without running a backup now.
Installs cron with apt if missing on Debian/Ubuntu, enables it at boot,
and checks it is running before saving the schedule. Use edit to preserve
unspecified saved settings; pause and resume control future runs only.`,
	Args:        cobra.NoArgs,
	Annotations: diagnostic(),
	RunE: func(*cobra.Command, []string) error {
		return withLock("schedule local backups", func() error { return backup.LocalSchedule(cfg) })
	},
}

var backupStatusCmd = &cobra.Command{
	Use: "status", Short: "Show the saved schedule and scheduled backup outcomes",
	Args: cobra.NoArgs, Annotations: diagnostic(),
	RunE: func(cmd *cobra.Command, _ []string) error {
		status, err := backup.BackupStatus()
		if err != nil {
			return err
		}
		if flagBackupJSON {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(status)
		}
		backup.WriteScheduleStatus(cmd.OutOrStdout(), status)
		return nil
	},
}

var backupScheduleEditCmd = &cobra.Command{
	Use: "edit", Short: "Edit selected settings, preserving a paused schedule",
	Args: cobra.NoArgs, Annotations: diagnostic(),
	RunE: func(cmd *cobra.Command, _ []string) error {
		changes := backup.ScheduleChanges{}
		if cmd.Flags().Changed("hour") {
			changes.Hour = &flagEditHour
		}
		if cmd.Flags().Changed("retention-days") {
			changes.RetentionDays = &flagEditRetention
		}
		if cmd.Flags().Changed("directory") {
			changes.Directory = &flagEditDirectory
		}
		if changes.Hour == nil && changes.RetentionDays == nil && changes.Directory == nil {
			return errors.New("specify --hour, --retention-days, or --directory to edit the saved schedule")
		}
		return withLock("edit backup schedule", func() error { return backup.EditLocalSchedule(changes) })
	},
}

func scheduleControlCommand(name string, enabled bool) *cobra.Command {
	return &cobra.Command{Use: name, Short: name + " future daily backups without changing stored files",
		Args: cobra.NoArgs, Annotations: diagnostic(),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := withLock(name+" backup schedule", func() error { return backup.SetScheduleEnabled(enabled) }); err != nil {
				return err
			}
			if enabled {
				fmt.Fprintln(cmd.OutOrStdout(), "Backups resumed; the next daily slot is the next attempt.")
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), "Backups paused; stored files are unchanged.")
			}
			return nil
		},
	}
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
		return finishInteractiveChange(withLock("restore", func() error { return restore.WithHealth(cfg, file) }), recoveryInteractive())
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
	if cmd.Flags().Changed("directory") {
		cfg.BackupDir = flagBackupDirectory
	}
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
	backupCmd.AddCommand(backupScheduleCmd, backupStatusCmd)
	backupStatusCmd.Flags().BoolVar(&flagBackupJSON, "json", false, "print structured schedule status")
	backupScheduleCmd.Flags().StringVar(&flagBackupDirectory, "directory", "", "absolute backup destination")
	backupScheduleEditCmd.Flags().IntVar(&flagEditHour, "hour", 0, "daily hour, 0-23 (server time)")
	backupScheduleEditCmd.Flags().IntVar(&flagEditRetention, "retention-days", 7, "retention, 1-30 days")
	backupScheduleEditCmd.Flags().StringVar(&flagEditDirectory, "directory", "", "absolute backup destination")
	backupScheduleCmd.AddCommand(backupScheduleEditCmd, scheduleControlCommand("pause", false), scheduleControlCommand("resume", true))
	backupCmd.Flags().BoolVar(&flagBackupScheduled, "scheduled", false, "internal tracked cron invocation")
	_ = backupCmd.Flags().MarkHidden("scheduled")
	backupCmd.Flags().BoolVar(&flagBackupSchedule, "schedule", false, "install a daily local-backup cron job after this backup")
	restoreCmd.Flags().StringVar(&flagRestoreFile, "file", "", "archive to restore (path or name in the backup directory)")
	rootCmd.AddCommand(backupCmd, restoreCmd, resyncCmd)
}
