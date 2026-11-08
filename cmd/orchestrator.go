package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/0x3639/nomctl/internal/orchestrator"
	"github.com/0x3639/nomctl/internal/service"
	"github.com/0x3639/nomctl/internal/tui"
)

var (
	flagOrchLogsFollow bool
	flagOrchLogsLines  int
)

var orchestratorCmd = &cobra.Command{
	Use:   "orchestrator",
	Short: "Operate the orchestrator service that runs next to a pillar",
	Long: `Functions for the Zenon orchestrator. They need the orchestrator
systemd unit (NOMCTL_ORCHESTRATOR_SERVICE, default "orchestrator") to exist on
this node; nodes without it get a clear refusal.`,
}

var orchestratorStatusCmd = &cobra.Command{
	Use:         "status",
	Short:       "Show whether the orchestrator is installed and running",
	Args:        cobra.NoArgs,
	Annotations: diagnostic(),
	RunE: func(cmd *cobra.Command, _ []string) error {
		ok, err := orchestrator.Installed(cfg)
		if err != nil {
			return err
		}
		if !ok {
			return orchestrator.ErrNotInstalled
		}
		st, err := service.Status(cfg.OrchestratorService)
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s: %s\n", cfg.OrchestratorService, st)
		return nil
	},
}

var orchestratorLogsCmd = &cobra.Command{
	Use:         "logs",
	Short:       "Show the orchestrator journal (use -f to follow)",
	Args:        cobra.NoArgs,
	Annotations: diagnostic(),
	RunE: func(*cobra.Command, []string) error {
		ok, err := orchestrator.Installed(cfg)
		if err != nil {
			return err
		}
		if !ok {
			return orchestrator.ErrNotInstalled
		}
		return tui.MonitorUnit(cfg.OrchestratorService, flagOrchLogsFollow, flagOrchLogsLines)
	},
}

var orchestratorHardResetCmd = &cobra.Command{
	Use:   "hard-reset",
	Short: "Stop the orchestrator, delete its queues and events, start it again",
	Long: `Ports the orchestrator hard-reset script: stops the orchestrator unit,
waits ten seconds, deletes queues/ and events/ under the orchestrator
directory (NOMCTL_ORCHESTRATOR_DIR, default /root/.orchestrator) and starts
the unit again. The orchestrator rebuilds both from the chain.

This command does not ask for confirmation; the interactive menu does.`,
	Args:        cobra.NoArgs,
	Annotations: rootOnly(),
	RunE: func(*cobra.Command, []string) error {
		return withLock("orchestrator hard-reset", func() error { return orchestrator.HardReset(cfg) })
	},
}

func init() {
	orchestratorLogsCmd.Flags().BoolVarP(&flagOrchLogsFollow, "follow", "f", false, "follow the journal in real time")
	orchestratorLogsCmd.Flags().IntVarP(&flagOrchLogsLines, "lines", "n", 20, "number of recent lines to show when not following")
	orchestratorCmd.AddCommand(orchestratorStatusCmd, orchestratorLogsCmd, orchestratorHardResetCmd)
	rootCmd.AddCommand(orchestratorCmd)
}
