package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/0x3639/nomctl/internal/alerts"
	"github.com/0x3639/nomctl/internal/metrics"
	"github.com/0x3639/nomctl/internal/producer"
	"github.com/0x3639/nomctl/internal/update"
)

var (
	flagStatusJSON bool
	flagStatusWait time.Duration
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show service, sync, process and host state in one screen",
	Long: `Prints one sample of everything nomctl top shows: systemd state, sync
state and heights from the node's local RPC, process CPU/memory/open files,
and host load, memory, disk and pressure. Read-only and safe at any time.
CPU %% and the sync rate are measured over --wait (default 2s).`,
	Args:        cobra.NoArgs,
	Annotations: diagnostic(),
	RunE: func(cmd *cobra.Command, _ []string) error {
		// CPU % and the sync rate are deltas, so take two samples --wait apart.
		sampler := metrics.NewSampler(cfg)
		sampler.SetPillarName(pillarName())
		ctx := context.Background()
		sampler.Take(ctx)
		time.Sleep(flagStatusWait)
		sample := sampler.Take(ctx)
		if flagStatusJSON {
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(sample)
		}
		fmt.Fprint(cmd.OutOrStdout(), metrics.Format(sample))
		if pc, err := producer.ReadConfig(producer.ConfigPath(cfg.ZnnDir)); err == nil && pc != nil {
			fmt.Fprintf(cmd.OutOrStdout(), "%-9s %s (wallet/%s, index %d)\n", "Producer", pc.Address, pc.KeyFilePath, pc.Index)
		}
		checkCtx, cancelCheck := context.WithTimeout(ctx, 20*time.Second)
		defer cancelCheck()
		for _, line := range updateLines(checkCtx, sample.Node.Commit) {
			fmt.Fprintf(cmd.OutOrStdout(), "%-9s %s\n", "Update", line)
		}
		return nil
	},
}

// updateLines runs the cached update check and renders any "Update" lines.
func updateLines(ctx context.Context, nodeCommit string) []string {
	if !cfg.UpdateCheck || flagNoUpdateCheck {
		return nil
	}
	c := update.Run(ctx, update.Options{Repo: cfg.ReleaseRepo, NodeRepo: cfg.RepoURL, NodeBranch: cfg.BranchName})
	return update.Lines(c, version, nodeCommit)
}

// pillarName prefers the alerts configuration, then NOMCTL_PILLAR_NAME.
func pillarName() string {
	if acfg, err := alerts.Load(alerts.DefaultConfigPath); err == nil && acfg.PillarName != "" {
		return acfg.PillarName
	}
	return cfg.PillarName
}

func init() {
	statusCmd.Flags().BoolVar(&flagStatusJSON, "json", false, "print the sample as JSON")
	statusCmd.Flags().BoolVar(&flagNoUpdateCheck, "no-update-check", false, "skip the GitHub update check (NOMCTL_UPDATE_CHECK=false)")
	statusCmd.Flags().DurationVar(&flagStatusWait, "wait", 2*time.Second, "measurement window for CPU %% and sync rate")
	rootCmd.AddCommand(statusCmd)
}
