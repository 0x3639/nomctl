package tui

import (
	"fmt"
	"os"

	"github.com/0x3639/nomctl/internal/backup"
	"github.com/0x3639/nomctl/internal/config"
	"github.com/charmbracelet/huh"
)

func BackupScheduleMenu(cfg *config.Config) error {
	for {
		status, err := backup.BackupStatus()
		if err != nil {
			return err
		}
		backup.WriteScheduleStatus(os.Stderr, status)
		options := []huh.Option[string]{}
		if status.Schedule == nil {
			options = append(options, huh.NewOption("Set up daily backups", "edit"))
		} else {
			options = append(options, huh.NewOption("Edit schedule", "edit"))
			if status.Schedule.Enabled {
				options = append(options, huh.NewOption("Pause scheduled backups", "pause"))
			} else {
				options = append(options, huh.NewOption("Resume scheduled backups", "resume"))
			}
		}
		options = append(options, huh.NewOption("Refresh status", "refresh"), huh.NewOption("Back", "back"))
		choice, err := Select("BACKUP STATUS & SCHEDULE:", options)
		if err != nil {
			return err
		}
		switch choice {
		case "back":
			return nil
		case "edit":
			if err := BackupSchedule(cfg); err != nil {
				return err
			}
		case "pause", "resume":
			if err := withLock(choice+" backup schedule", func() error { return backup.SetScheduleEnabled(choice == "resume") }); err != nil {
				return err
			}
			fmt.Fprintln(os.Stderr, "Schedule updated. Stored backups and restore points are unchanged.")
		}
	}
}
