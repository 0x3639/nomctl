package cmd

import (
	"errors"

	"github.com/0x3639/nomctl/internal/tui"
	"github.com/0x3639/nomctl/internal/ui"
)

// runTUI launches the interactive menu; without a terminal it prints help.
func runTUI() error {
	if !ui.Interactive() {
		return errors.New("no terminal detected; run a subcommand instead (see nomctl --help)")
	}
	return tui.Run(cfg)
}
