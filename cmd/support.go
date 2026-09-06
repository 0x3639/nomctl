package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/spf13/cobra"

	"github.com/0x3639/nomctl/internal/logx"
	"github.com/0x3639/nomctl/internal/support"
)

var (
	flagSupportOut     string
	flagSupportSince   string
	flagSupportWatch   bool
	flagSupportPoll    time.Duration
	flagSupportTimeout time.Duration
)

var supportCmd = &cobra.Command{
	Use:   "support-bundle",
	Short: "Collect diagnostics into a shareable .tar.gz",
	Long: `Collects systemd state, journals, process and cgroup details, host
resources, node log tails, a node RPC snapshot (peer IPs redacted) and
nomctl's own state into a directory and a .tar.gz beside it. Read-only:
it never stops the node or touches its data. Config contents are never
collected. With --watch it first samples the live process until systemd
restarts it, so the bundle contains the moments before a crash.`,
	Args:        cobra.NoArgs,
	Annotations: diagnostic(),
	RunE: func(cmd *cobra.Command, _ []string) error {
		out := flagSupportOut
		if out == "" {
			out = support.DefaultOutputDir(time.Now())
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		if flagSupportWatch {
			if flagSupportPoll < time.Second {
				return errors.New("--poll must be at least 1s")
			}
			if err := support.Watch(ctx, cfg, support.WatchOptions{Poll: flagSupportPoll, Timeout: flagSupportTimeout, Out: out}); err != nil {
				return err
			}
			// A Ctrl+C that ended the watch must not abort the collection.
			stop()
			ctx = context.Background()
		}
		res, err := support.Collect(ctx, cfg, support.Options{OutputDir: out, Since: flagSupportSince, Version: versionString()})
		if err != nil {
			return err
		}
		logx.Success("Support bundle created")
		printBundleResult(cmd, res)
		return nil
	},
}

func printBundleResult(cmd *cobra.Command, res support.Result) {
	w := cmd.OutOrStdout()
	fmt.Fprintf(w, "\nDiagnostics directory: %s\nBundle:                %s\nCrash markers:         %s\n\n", res.Dir, res.Archive, res.Markers)
	fmt.Fprintln(w, "The bundle may contain the host name, node addresses, paths and service arguments.")
	fmt.Fprintln(w, "Review it before sharing publicly. Configuration contents are not collected.")
}

func init() {
	supportCmd.Flags().StringVar(&flagSupportOut, "output", "", "diagnostics directory (default /root/nomctl-support-<host>-<time>)")
	supportCmd.Flags().StringVar(&flagSupportSince, "since", support.DefaultSince, "journal window passed to journalctl --since")
	supportCmd.Flags().BoolVar(&flagSupportWatch, "watch", false, "sample the live process until systemd restarts it, then collect")
	supportCmd.Flags().DurationVar(&flagSupportPoll, "poll", 10*time.Second, "watch sampling interval")
	supportCmd.Flags().DurationVar(&flagSupportTimeout, "timeout", 0, "stop watching after this long (0 = no timeout)")
	rootCmd.AddCommand(supportCmd)
}
