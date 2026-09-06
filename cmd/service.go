package cmd

import (
	"github.com/spf13/cobra"

	"github.com/0x3639/nomctl/internal/service"
	"github.com/0x3639/nomctl/internal/tui"
)

var (
	flagLogsFollow bool
	flagLogsLines  int
)

var startCmd = &cobra.Command{
	Use:         "start",
	Short:       "Start the node service",
	Args:        cobra.NoArgs,
	Annotations: rootOnly(),
	RunE:        func(*cobra.Command, []string) error { return service.Start(cfg.ServiceName) },
}

var stopCmd = &cobra.Command{
	Use:         "stop",
	Short:       "Stop the node service",
	Args:        cobra.NoArgs,
	Annotations: rootOnly(),
	RunE:        func(*cobra.Command, []string) error { return service.Stop(cfg.ServiceName) },
}

var restartCmd = &cobra.Command{
	Use:         "restart",
	Short:       "Restart the node service",
	Args:        cobra.NoArgs,
	Annotations: rootOnly(),
	RunE:        func(*cobra.Command, []string) error { return service.Restart(cfg.ServiceName) },
}

var logsCmd = &cobra.Command{
	Use:         "logs",
	Short:       "Show the node service journal (use -f to follow)",
	Args:        cobra.NoArgs,
	Annotations: rootOnly(),
	RunE: func(*cobra.Command, []string) error {
		return tui.Monitor(cfg, flagLogsFollow, flagLogsLines)
	},
}

func init() {
	logsCmd.Flags().BoolVarP(&flagLogsFollow, "follow", "f", false, "follow the journal in real time")
	logsCmd.Flags().IntVarP(&flagLogsLines, "lines", "n", 20, "number of recent lines to show when not following")
	rootCmd.AddCommand(startCmd, stopCmd, restartCmd, logsCmd)
}
