package tui

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/0x3639/nomctl/internal/backup"
	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/ui"
	"github.com/charmbracelet/huh"
)

// ManageBackups previews exact ordinary archives before deleting them. Restore
// points never enter this list, even when they are older than the chosen age.
func ManageBackups(cfg config.Config) error {
	for {
		infos, err := backup.List(cfg)
		if err != nil {
			return err
		}
		if len(infos) == 0 {
			fmt.Fprintln(os.Stderr, "No completed local backups.")
			return nil
		}
		action, err := Select("MANAGE BACKUPS:", []huh.Option[string]{huh.NewOption("Delete one selected backup", "delete"), huh.NewOption("Prune backups older than a chosen number of days", "prune"), huh.NewOption("Back", "back")})
		if err != nil {
			return err
		}
		if action == "back" {
			return nil
		}
		var selected []backup.Info
		if action == "delete" {
			var path string
			var opts []huh.Option[string]
			for _, info := range infos {
				opts = append(opts, huh.NewOption(info.Name()+" · "+info.ModTime.Format("2006-01-02 15:04"), info.Path))
			}
			sel := huh.NewSelect[string]().Title("SELECT BACKUP TO DELETE:").Options(opts...).Value(&path).Height(12)
			if err := runForm(sel); err != nil {
				return err
			}
			for _, info := range infos {
				if info.Path == path {
					selected = []backup.Info{info}
				}
			}
		} else {
			text, err := Input("Prune backups older than how many days? (1-30)", strconv.Itoa(cfg.BackupRetentionDays))
			if err != nil {
				return err
			}
			if strings.TrimSpace(text) == "" {
				text = strconv.Itoa(cfg.BackupRetentionDays)
			}
			days, ok := ParseMaxBackups(text)
			if !ok {
				fmt.Fprintln(os.Stderr, "Enter a number from 1 to 30.")
				continue
			}
			cutoff := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
			for _, info := range infos {
				if !info.ModTime.After(cutoff) {
					selected = append(selected, info)
				}
			}
		}
		if len(selected) == 0 {
			fmt.Fprintln(os.Stderr, "No backups match that age.")
			continue
		}
		var bytes int64
		var preview strings.Builder
		for _, info := range selected {
			fmt.Fprintf(&preview, "\n• %s", info.Name())
			for _, path := range []string{info.Path, backup.HashPath(info.Path)} {
				st, err := os.Stat(path)
				if err != nil {
					return err
				}
				bytes += st.Size()
			}
		}
		ok, err := Confirm(fmt.Sprintf("Permanently delete these %d backup(s), freeing approximately %.1f MiB?%s\n\nRestore points are not included. This cannot be undone.", len(selected), float64(bytes)/(1<<20), preview.String()))
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		if err := withLock("delete selected backups", func() error {
			for _, info := range selected {
				if err := backup.DeleteBackup(cfg, info.Path); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}
		ui.Success("Selected backups deleted")
	}
}
