package tui

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/0x3639/nomctl/internal/backup"
	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/restore"
	"github.com/0x3639/nomctl/internal/ui"
	"github.com/charmbracelet/huh"
)

func ChooseRestorePointKeep(cfg *config.Config) error {
	keep := "indefinite"
	if cfg.RestorePointKeepDays > 0 {
		keep = "days"
	}
	sel := huh.NewSelect[string]().Title("How long should this restore point be kept?").
		Description("Restore points are deleted only through this menu. Days mark eligibility for manual pruning.").
		Options(huh.NewOption("Indefinite — delete individually when no longer needed", "indefinite"), huh.NewOption("Choose days — eligible for manual pruning afterward", "days")).Value(&keep)
	if err := runForm(sel); err != nil {
		return err
	}
	if keep == "indefinite" {
		cfg.RestorePointKeepDays = 0
		return nil
	}
	def := cfg.RestorePointKeepDays
	if def == 0 {
		def = 7
	}
	for {
		text, err := Input("Days before eligible for manual pruning (1-30)", strconv.Itoa(def))
		if err != nil {
			return err
		}
		if strings.TrimSpace(text) == "" {
			text = strconv.Itoa(def)
		}
		days, err := backup.ParseRestorePointKeep(text)
		if err != nil || days == 0 {
			slog.Warn("Enter a number from 1 to 30.")
			continue
		}
		cfg.RestorePointKeepDays = days
		return nil
	}
}

func CreateRestorePoint(cfg config.Config) error {
	label, err := Input("Restore point name (optional)", "Manual restore point")
	if err != nil {
		return err
	}
	label = strings.TrimSpace(label)
	if label == "" {
		label = "Manual restore point"
	}
	if err := ChooseRestorePointKeep(&cfg); err != nil {
		return err
	}
	ok, err := Confirm("Create a protected restore point?\nChecks disk space, stops the node to copy chain data, then restarts it before compression.\nIncludes the matching node binary. Wallet and config use their separate encrypted backup.")
	if err != nil {
		return err
	}
	if !ok {
		return ErrCancelled
	}
	return withLock("create restore point", func() error { _, err := backup.RunRestorePoint(cfg, label); return err })
}

func pointLabel(p backup.RestorePoint, now time.Time) string {
	keep := "indefinite"
	if p.KeepDays > 0 {
		keep = fmt.Sprintf("keep %d days", p.KeepDays)
	}
	if p.Eligible(now) {
		keep = "eligible for manual pruning"
	}
	if !p.Complete || p.Problem != "" {
		keep = "incomplete / needs inspection"
	}
	return fmt.Sprintf("%s · %s · %.1f MiB · %s", p.ID, p.Label, float64(p.Size)/(1<<20), keep)
}

func pickRestorePoint(cfg config.Config, completeOnly bool) (backup.RestorePoint, error) {
	points, err := backup.ListRestorePoints(cfg)
	if err != nil {
		return backup.RestorePoint{}, err
	}
	opts := []huh.Option[string]{}
	for _, p := range points {
		if !completeOnly || (p.Complete && p.Problem == "") {
			opts = append(opts, huh.NewOption(pointLabel(p, time.Now()), p.ID))
		}
	}
	if len(opts) == 0 {
		return backup.RestorePoint{}, fmt.Errorf("no matching restore points in %s", backup.RestorePointsDir(cfg))
	}
	var id string
	sel := huh.NewSelect[string]().Title("SELECT RESTORE POINT:").Options(opts...).Value(&id).Height(12)
	if err := runForm(sel); err != nil {
		return backup.RestorePoint{}, err
	}
	for _, p := range points {
		if p.ID == id {
			return p, nil
		}
	}
	return backup.RestorePoint{}, ErrCancelled
}

func RestorePoint(cfg config.Config) error {
	p, err := pickRestorePoint(cfg, true)
	if err != nil {
		return err
	}
	ok, err := Confirm(fmt.Sprintf("Restore %s?\n\nReplace chain data AND the node binary with this matching restore point.\nCurrent data and binary are kept as recovery copies; wallet and config stay unchanged.", p.ID))
	if err != nil {
		return err
	}
	if !ok {
		return ErrCancelled
	}
	return withLock("restore from restore point", func() error { return restore.RunRestorePoint(cfg, p.ID) })
}

func ManageRestorePoints(cfg config.Config) error {
	for {
		points, err := backup.ListRestorePoints(cfg)
		if err != nil {
			return err
		}
		if len(points) == 0 {
			fmt.Fprintln(os.Stderr, "No restore points have been created.")
			return nil
		}
		var total int64
		for _, p := range points {
			fmt.Fprintln(os.Stderr, pointLabel(p, time.Now()))
			total += p.Size
		}
		fmt.Fprintf(os.Stderr, "Total restore-point storage: %.1f MiB\n", float64(total)/(1<<20))
		action, err := Select("MANAGE RESTORE POINTS:", []huh.Option[string]{huh.NewOption("Delete one selected restore point", "delete"), huh.NewOption("Prune points whose chosen retention has elapsed", "prune"), huh.NewOption("Back", "back")})
		if err != nil {
			return err
		}
		if action == "back" {
			return nil
		}
		var selected []backup.RestorePoint
		if action == "delete" {
			p, err := pickRestorePoint(cfg, false)
			if err != nil {
				return err
			}
			selected = []backup.RestorePoint{p}
		} else {
			for _, p := range points {
				if p.Eligible(time.Now()) {
					selected = append(selected, p)
				}
			}
		}
		if len(selected) == 0 {
			fmt.Fprintln(os.Stderr, "No restore points are eligible. Indefinite and incomplete points must be selected individually.")
			continue
		}
		var preview strings.Builder
		var bytes int64
		for _, p := range selected {
			fmt.Fprintf(&preview, "\n• %s", p.ID)
			bytes += p.Size
		}
		ok, err := Confirm(fmt.Sprintf("Permanently delete these %d restore point(s), freeing approximately %.1f MiB?%s\n\nThis cannot be undone.", len(selected), float64(bytes)/(1<<20), preview.String()))
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		err = withLock("delete selected restore points", func() error {
			return deleteSelectedRestorePoints(cfg, selected, action == "prune", time.Now())
		})
		if err != nil {
			return err
		}
		ui.Success("Selected restore points deleted")
	}
}

// deleteSelectedRestorePoints runs under the operation lock after confirmation.
// Validate the entire preview before deleting anything: an upgrade may have
// completed an incomplete point while the confirmation was on screen.
func deleteSelectedRestorePoints(cfg config.Config, selected []backup.RestorePoint, prune bool, now time.Time) error {
	points, err := backup.ListRestorePoints(cfg)
	if err != nil {
		return err
	}
	current := make(map[string]backup.RestorePoint, len(points))
	for _, p := range points {
		current[p.ID] = p
	}
	for _, preview := range selected {
		p, exists := current[preview.ID]
		if !exists || p != preview || (prune && !p.Eligible(now)) {
			return fmt.Errorf("restore point %s changed or is no longer eligible; nothing deleted — review the list again", preview.ID)
		}
	}
	// Delete precisely the previewed IDs, never a newly expanded list.
	for _, p := range selected {
		if err := backup.DeleteRestorePoint(cfg, p.ID); err != nil {
			return err
		}
	}
	return nil
}
