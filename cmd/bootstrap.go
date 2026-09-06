package cmd

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/0x3639/nomctl/internal/bootstrap"
)

var flagBootstrapDiscard bool

var bootstrapCmd = &cobra.Command{
	Use:   "bootstrap [URL]",
	Short: "Replace chain data with a published snapshot so the node syncs in minutes",
	Long: `Downloads a chain snapshot (a .zip with a .hash sidecar next to it),
verifies its SHA-256, stops the node, swaps in the snapshot's nom, network and
consensus directories and starts the node again. The wallet and config.json
are not touched.

The previous chain data is kept under <backup dir>/restore so it can be put
back; delete it once the node has synced, or pass --discard to delete it up
front on disks too small for both copies.

URL defaults to NOMCTL_BOOTSTRAP_URL. The download lands in
<backup dir>/bootstrap and is removed on success; a verified download left by
a failed run is reused.

This command does not ask for confirmation; the interactive menu does.`,
	Args:        cobra.MaximumNArgs(1),
	Annotations: rootOnly(),
	RunE: func(_ *cobra.Command, args []string) error {
		url := cfg.BootstrapURL
		if len(args) == 1 {
			url = args[0]
		}
		if err := bootstrap.ValidateURL(url); err != nil {
			return err
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return withLock("bootstrap", func() error {
			return bootstrap.Run(ctx, cfg, bootstrap.Options{URL: url, Discard: flagBootstrapDiscard})
		})
	},
}

func init() {
	bootstrapCmd.Flags().BoolVar(&flagBootstrapDiscard, "discard", false, "delete the current chain data instead of keeping a copy under the backup directory")
	rootCmd.AddCommand(bootstrapCmd)
}
