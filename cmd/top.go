package cmd

import (
	"errors"
	"time"

	"github.com/spf13/cobra"

	"github.com/0x3639/nomctl/internal/tui"
	"github.com/0x3639/nomctl/internal/ui"
)

var flagTopInterval time.Duration

var topCmd = &cobra.Command{
	Use:         "top",
	Short:       "Live dashboard of sync, process and host metrics",
	Long:        "Refreshes the nomctl status view every --interval in the terminal. Press q to quit.",
	Args:        cobra.NoArgs,
	Annotations: diagnostic(),
	RunE: func(*cobra.Command, []string) error {
		if !ui.Interactive() {
			return errors.New("nomctl top needs a terminal; use nomctl status for scripts")
		}
		if flagTopInterval < 500*time.Millisecond {
			return errors.New("--interval must be at least 500ms")
		}
		return tui.Top(cfg, flagTopInterval, pillarName())
	},
}

func init() {
	topCmd.Flags().DurationVar(&flagTopInterval, "interval", 2*time.Second, "refresh interval")
	rootCmd.AddCommand(topCmd)
}
