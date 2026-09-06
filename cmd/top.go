package cmd

import (
	"context"
	"errors"
	"time"

	"github.com/spf13/cobra"

	"github.com/0x3639/nomctl/internal/tui"
	"github.com/0x3639/nomctl/internal/ui"
	"github.com/0x3639/nomctl/internal/update"
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
		var notes []string
		if cfg.UpdateCheck {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			c := update.Run(ctx, update.Options{Repo: cfg.ReleaseRepo, NodeRepo: cfg.RepoURL, NodeBranch: cfg.BranchName})
			cancel()
			notes = update.Lines(c, version, "")
			tui.NodeRemoteCommit = c.NodeRemote
			tui.NodeBranch = c.NodeBranch
		}
		return tui.Top(cfg, flagTopInterval, pillarName(), notes)
	},
}

func init() {
	topCmd.Flags().DurationVar(&flagTopInterval, "interval", 2*time.Second, "refresh interval")
	rootCmd.AddCommand(topCmd)
}
