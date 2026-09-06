package cmd

import "github.com/spf13/cobra"

// runTUI launches the interactive menu. Implemented in phase 6.
func runTUI(cmd *cobra.Command) error {
	return cmd.Help()
}
