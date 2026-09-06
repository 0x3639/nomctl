package cmd

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/spf13/cobra"

	"github.com/hypercore-one/nomctl/internal/service"
	"github.com/hypercore-one/nomctl/internal/ui"
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
		return showLogs(flagLogsFollow, flagLogsLines)
	},
}

// showLogs ports monitor.sh: following a stopped service falls back to the
// last lines with a warning.
func showLogs(follow bool, lines int) error {
	name := cfg.ServiceName
	if follow && !service.IsActive(name) {
		slog.Warn(fmt.Sprintf("%s service is not running. Showing last %d log lines:", name, lines))
		follow = false
	}
	if follow {
		fmt.Fprintln(os.Stderr, ui.StyleBox.Render(fmt.Sprintf("Monitoring %s logs. Press Ctrl+C to stop.", name)))
	}
	return service.Logs(name, follow, lines)
}

func init() {
	logsCmd.Flags().BoolVarP(&flagLogsFollow, "follow", "f", false, "follow the journal in real time")
	logsCmd.Flags().IntVarP(&flagLogsLines, "lines", "n", 20, "number of recent lines to show when not following")
	rootCmd.AddCommand(startCmd, stopCmd, restartCmd, logsCmd)
}
