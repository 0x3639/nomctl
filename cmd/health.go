package cmd

import (
	"encoding/json"
	"errors"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/0x3639/nomctl/internal/backup"
	"github.com/0x3639/nomctl/internal/health"
	"github.com/0x3639/nomctl/internal/tui"
	"github.com/0x3639/nomctl/internal/ui"
	"github.com/spf13/cobra"
)

func finishInteractiveChange(err error, interactive bool) error {
	if err != nil && interactive {
		return tui.RecoverChange(cfg, err)
	}
	return err
}

func init() {
	var window time.Duration
	var jsonOutput bool
	c := &cobra.Command{Use: "health", Short: "Observe node progress, peers and znnd identity over a bounded window", Args: cobra.NoArgs, Annotations: verificationAnnotations(), RunE: func(cmd *cobra.Command, _ []string) error {
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		before := health.Observe(ctx, cfg)
		expected, err := backup.SHA256File(cfg.BinaryPath())
		if err != nil {
			return err
		}
		r, err := health.Check(ctx, cfg, before, expected, window)
		if jsonOutput {
			return errors.Join(err, json.NewEncoder(cmd.OutOrStdout()).Encode(r))
		}
		health.Write(cmd.OutOrStdout(), r)
		return err
	}}
	c.Flags().DurationVar(&window, "wait", health.DefaultWindow, "observation window, 1s to 5m")
	c.Flags().BoolVar(&jsonOutput, "json", false, "print structured before/after evidence")
	rootCmd.AddCommand(c)
}

func recoveryInteractive() bool { return ui.Interactive() }
