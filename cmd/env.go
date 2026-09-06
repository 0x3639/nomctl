package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/ui"
)

var envCmd = &cobra.Command{
	Use:   "env",
	Short: "List every NOMCTL_* environment variable and its default",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		out := cmd.OutOrStdout()
		for _, v := range config.Vars() {
			fmt.Fprintf(out, "%s %s\n", ui.Pad(v.Name, 32), ui.Pad(v.Default, 44)+" "+v.Description)
		}
		return nil
	},
}

func init() { rootCmd.AddCommand(envCmd) }
