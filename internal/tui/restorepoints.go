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
	return chooseRestorePointKeep(cfg, "How long should this restore point be kept?", "")
}

// ChooseUpgradeRestorePointKeep explains the mandatory recovery copy before
// asking for retention, for both signed-release and source-build upgrades.
func ChooseUpgradeRestorePointKeep(cfg *config.Config) error {
	return chooseRestorePointKeep(cfg, "A restore point is required before upgrading your node software.",
		"nomctl creates it automatically during the upgrade. It backs up your current\n"+
			"chain data and installed znnd so you can restore them if something goes wrong.\n"+
			"It records the znnd version and commit when available, plus its SHA-256 checksum.\n"+
			"Wallet and config use a separate encrypted backup.\n\n"+
			"Choose how to keep this required restore point:\n")
}

func chooseRestorePointKeep(cfg *config.Config, title, introduction string) error {
	keep := "indefinite"
	if cfg.RestorePointKeepDays > 0 {
		keep = "days"
	}
	sel := huh.NewSelect[string]().Title(title).
		Description(introduction+"Indefinite: keep until you select and delete it individually.\n"+
			"Choose days (1–30): after that time, you can remove it using manual pruning.\n"+
			"Neither option deletes it automatically. Delete or prune in Manage restore points.").
		Options(huh.NewOption("Indefinite — keep until I delete it", "indefinite"), huh.NewOption("Choose days (1–30) — allow manual pruning afterward", "days")).Value(&keep)
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
	ok, err := Confirm("Back up chain data AND the installed znnd into a protected restore point?\nRecords the znnd version and commit hash when available, plus its SHA-256 checksum.\nChecks disk space, stops the node to copy data, then restarts it before compression.\nWallet and config use their separate encrypted backup.")
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
	version := p.BinaryVersion
	if version == "" {
		version = "version unavailable"
	}
	return fmt.Sprintf("%s · %s · znnd %s · %.1f MiB · %s", p.ID, p.Label, version, float64(p.Size)/(1<<20), keep)
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
	sel.DescriptionFunc(func() string {
		for _, p := range points {
			if p.ID == id {
				return p.BinaryDescription()
			}
		}
		return "Contains chain data and the saved znnd."
	}, &id)
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
	return confirmRestorePoint(cfg, p)
}

// RestorePointByID gives interactive CLI restores the same binary choice and
// confirmation as the menu picker.
func RestorePointByID(cfg config.Config, id string) error {
	p, err := backup.ReadRestorePoint(cfg, id)
	if err != nil {
		return err
	}
	return confirmRestorePoint(cfg, p)
}

func confirmRestorePoint(cfg config.Config, p backup.RestorePoint) error {
	if !p.Complete {
		return fmt.Errorf("restore point is incomplete")
	}
	withBinary := true
	sel := huh.NewSelect[bool]().Title("Also revert znnd to the version saved in this restore point?").
		Description(p.BinaryDescription()).
		Options(huh.NewOption("Yes — restore chain data and saved znnd (recommended)", true), huh.NewOption("No — restore data only; keep installed znnd", false)).Value(&withBinary)
	if err := runForm(sel); err != nil {
		return err
	}
	change := "Restore chain data AND the saved znnd. Current data and znnd are kept as recovery copies."
	if !withBinary {
		change = "Restore chain data only. Keep the installed znnd unchanged.\nThe installed znnd must support this older data; incompatible versions may fail to start. Current data is kept as a recovery copy."
	}
	ok, err := Confirm(fmt.Sprintf("Restore %s?\n\n%s\n\n%s\nWallet and configuration stay unchanged.", p.ID, p.BinaryDescription(), change))
	if err != nil {
		return err
	}
	if !ok {
		return ErrCancelled
	}
	return withLock("restore from restore point", func() error {
		return restore.RunRestorePointWithOptions(cfg, p.ID, restore.RestorePointOptions{DataOnly: !withBinary})
	})
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
			fmt.Fprintln(os.Stderr, p.BinaryDescription())
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
