package cmd

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/0x3639/nomctl/internal/metrics"
)

var flagStatusJSON bool

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show service, sync, process and host state in one screen",
	Long: `Prints one sample of everything nomctl top shows: systemd state, sync
state and heights from the node's local RPC, process CPU/memory/open files,
and host load, memory, disk and pressure. Read-only and safe at any time.`,
	Args:        cobra.NoArgs,
	Annotations: diagnostic(),
	RunE: func(cmd *cobra.Command, _ []string) error {
		sample := metrics.NewSampler(cfg).Take(context.Background())
		if flagStatusJSON {
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(sample)
		}
		fmt.Fprint(cmd.OutOrStdout(), metrics.Format(sample))
		return nil
	},
}

func init() {
	statusCmd.Flags().BoolVar(&flagStatusJSON, "json", false, "print the sample as JSON")
	rootCmd.AddCommand(statusCmd)
}
