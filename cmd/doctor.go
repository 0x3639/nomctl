package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/doctor"
	"github.com/spf13/cobra"
)

var doctorCmd = newDoctorCommand(doctor.Run)

func newDoctorCommand(run func(context.Context, config.Config, doctor.Options) (doctor.Report, error)) *cobra.Command {
	opts := doctor.Options{Wait: doctor.DefaultWait}
	var asJSON bool
	c := &cobra.Command{
		Use:   "doctor",
		Short: "Check my node: explain health problems and show znnd version and hashes",
		Long: `Collect read-only diagnostics for service state, RPC, chain progress,
peers, disk space, clock synchronization, P2P listening and recent log errors.
Shows znnd's RPC version/commit and installed/running executable SHA-256.
Unavailable evidence is reported as unknown. No services or settings change.

Outside reachability stays unknown unless both --probe-url and --public-ip
are supplied. The selected HTTPS endpoint receives the given public IP and
configured P2P port; see the doctor guide for the probe response contract.

Exit 0 means a report was produced, even when findings need attention.
For automation, inspect the severity of each check in --json output.`,
		Args:        cobra.NoArgs,
		Annotations: diagnostic(),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := opts.Validate(); err != nil {
				return err
			}
			if opts.ProbeURL != "" {
				fmt.Fprintf(cmd.ErrOrStderr(), "External probe: will contact %s with public IP %s and the configured P2P port, if available.\n", opts.ProbeURL, opts.PublicIP)
			}
			if !asJSON {
				fmt.Fprintf(cmd.ErrOrStderr(), "Checking node health; observing progress for %s…\n", opts.Wait)
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			r, err := run(ctx, cfg, opts)
			if err != nil {
				return err
			}
			return doctor.Write(cmd.OutOrStdout(), r, asJSON)
		},
	}
	c.Annotations[annotationOptionalServiceTools] = "true"
	c.Flags().BoolVar(&asJSON, "json", false, "print the diagnostic report as JSON")
	c.Flags().DurationVar(&opts.Wait, "wait", doctor.DefaultWait, "gap between observations (1s–2m)")
	c.Flags().StringVar(&opts.ProbeURL, "probe-url", "", "optional HTTPS reachability endpoint; sends --public-ip and the P2P port")
	c.Flags().StringVar(&opts.PublicIP, "public-ip", "", "this node's public IP to send to the optional external probe")
	return c
}

func init() { rootCmd.AddCommand(doctorCmd) }
