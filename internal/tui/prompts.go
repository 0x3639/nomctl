// Package tui implements the interactive menu (a port of menu.sh) and the
// prompts used by individual actions, built on huh and lipgloss.
package tui

import (
	"errors"
	"fmt"

	"github.com/charmbracelet/huh"

	"github.com/0x3639/nomctl/internal/backup"
	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/ui"
)

// ErrCancelled is returned when the user aborts a prompt.
var ErrCancelled = errors.New("cancelled")

// maxPickerEntries bounds the restore picker like `head -n 50` did.
const maxPickerEntries = 50

// PickBackup lists the newest archives and returns the chosen path.
func PickBackup(cfg config.Config) (string, error) {
	infos, err := backup.List(cfg)
	if err != nil {
		return "", err
	}
	if len(infos) == 0 {
		return "", fmt.Errorf("no backups found for %s in %s", cfg.ServiceName, cfg.BackupDir)
	}
	if len(infos) > maxPickerEntries {
		infos = infos[:maxPickerEntries]
	}
	opts := make([]huh.Option[string], 0, len(infos))
	for _, i := range infos {
		opts = append(opts, huh.NewOption(i.Name(), i.Path))
	}
	var chosen string
	sel := huh.NewSelect[string]().
		Title(ui.StyleHeader.Render("SELECT BACKUP TO RESTORE:")).
		Options(opts...).
		Height(15).
		Value(&chosen)
	if err := runForm(sel); err != nil {
		return "", err
	}
	return chosen, nil
}

// Confirm asks a yes/no question.
func Confirm(question string) (bool, error) {
	var ok bool
	c := huh.NewConfirm().Title(question).Affirmative("Yes").Negative("No").Value(&ok)
	if err := runForm(c); err != nil {
		return false, err
	}
	return ok, nil
}

// Input asks for a single line of text.
func Input(title, placeholder string) (string, error) {
	var v string
	in := huh.NewInput().Title(title).Placeholder(placeholder).Value(&v)
	if err := runForm(in); err != nil {
		return "", err
	}
	return v, nil
}

// Select asks the user to choose one of the labelled values.
func Select(title string, options []huh.Option[string]) (string, error) {
	var v string
	sel := huh.NewSelect[string]().Title(ui.StyleHeader.Render(title)).Options(options...).Height(15).Value(&v)
	if err := runForm(sel); err != nil {
		return "", err
	}
	return v, nil
}

func runForm(field huh.Field) error {
	form := huh.NewForm(huh.NewGroup(field)).WithTheme(theme())
	if err := form.Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return ErrCancelled
		}
		return err
	}
	return nil
}

// theme adapts huh's base theme to the gum colours of the bash version:
// green (46) cursor and selection, grey (242) titles.
func theme() *huh.Theme {
	t := huh.ThemeBase()
	t.Focused.Title = t.Focused.Title.Foreground(ui.ColorMuted)
	t.Focused.SelectSelector = t.Focused.SelectSelector.Foreground(ui.ColorAccent)
	t.Focused.SelectedOption = t.Focused.SelectedOption.Foreground(ui.ColorAccent)
	t.Focused.FocusedButton = t.Focused.FocusedButton.Foreground(ui.ColorBlack).Background(ui.ColorAccent)
	t.Focused.BlurredButton = t.Focused.BlurredButton.Foreground(ui.ColorWhite)
	t.Focused.TextInput.Cursor = t.Focused.TextInput.Cursor.Foreground(ui.ColorAccent)
	t.Focused.TextInput.Prompt = t.Focused.TextInput.Prompt.Foreground(ui.ColorAccent)
	t.Blurred = t.Focused
	return t
}
