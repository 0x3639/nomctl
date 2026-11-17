package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/0x3639/nomctl/internal/migrate"
	"github.com/0x3639/nomctl/internal/tui"
	"github.com/0x3639/nomctl/internal/ui"
	"github.com/spf13/cobra"
)

func migrationConfirm(yes bool, prompt string) error {
	if yes {
		return nil
	}
	if !ui.Interactive() {
		return errors.New("review migrate status, then pass --yes to confirm this step")
	}
	ok, err := tui.Confirm(prompt)
	if err != nil {
		return err
	}
	if !ok {
		return errCancelled
	}
	return nil
}

func newMigrateCommand() *cobra.Command {
	c := &cobra.Command{Use: "migrate", Short: "Move the entire node directory to a new device using rsync over SSH", Args: cobra.NoArgs, Annotations: diagnostic(), RunE: func(cmd *cobra.Command, _ []string) error { return tui.MigrationMenu(cfg) }}
	var conn migrate.Connection
	var dest migrate.Installation
	plan := &cobra.Command{Use: "plan", Short: "Check and save a migration preview without stopping either node", Args: cobra.NoArgs, Annotations: diagnostic(), RunE: func(cmd *cobra.Command, _ []string) error {
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		p, err := migrate.New().Plan(ctx, cfg, conn, dest)
		if err != nil {
			return err
		}
		migrate.WritePlan(cmd.OutOrStdout(), p)
		return nil
	}}
	plan.Flags().StringVar(&conn.Host, "to", "", "trusted SSH destination, such as root@new-server")
	plan.Flags().IntVar(&conn.Port, "port", 22, "SSH port")
	plan.Flags().StringVar(&conn.Nomctl, "remote-nomctl", "/usr/local/bin/nomctl", "installed migration-capable nomctl on the destination")
	plan.Flags().BoolVar(&conn.Sudo, "sudo", false, "use noninteractive sudo for remote nomctl")
	plan.Flags().StringVar(&dest.Data, "destination-data", "/root/.znn", "new data directory (must not exist)")
	plan.Flags().StringVar(&dest.Binary, "destination-binary", "/usr/local/bin/znnd", "new executable path (must not exist)")
	plan.Flags().StringVar(&dest.Service, "destination-service", "go-zenon", "new systemd service name")
	c.AddCommand(plan)
	var jsonOutput bool
	status := &cobra.Command{Use: "status", Short: "Show saved progress and current node start blocks on both hosts", Args: cobra.NoArgs, Annotations: diagnostic(), RunE: func(cmd *cobra.Command, _ []string) error {
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		r, err := migrate.New().Inspect(ctx)
		if err != nil {
			return err
		}
		if jsonOutput {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(r)
		}
		migrate.WriteStatus(cmd.OutOrStdout(), r)
		return nil
	}}
	status.Flags().BoolVar(&jsonOutput, "json", false, "print structured status and observed evidence")
	c.AddCommand(status)
	for _, action := range []string{"transfer", "handoff", "return", "cancel"} {
		var yes bool
		step := &cobra.Command{Use: action, Args: cobra.NoArgs, Annotations: diagnostic()}
		if action == "transfer" {
			step.Aliases = []string{"resume"}
		}
		step.Short = map[string]string{"transfer": "Preserve recovery files, stop both nodes, and copy or resume", "handoff": "Confirm the replacement may start; keep the original blocked", "return": "Block and stop the replacement before restarting the original", "cancel": "Discard a preview before any node changes"}[action]
		step.Flags().BoolVar(&yes, "yes", false, "confirm this migration step")
		step.RunE = func(cmd *cobra.Command, _ []string) error {
			co := migrate.New()
			saved, err := co.Local.Status()
			if err != nil {
				return err
			}
			migrate.WriteState(cmd.OutOrStdout(), saved)
			if err := migrationConfirm(yes, step.Short+"?"); err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			if action == "cancel" {
				return co.CancelPlan(ctx)
			}
			var result migrate.State
			switch action {
			case "transfer":
				pass := ""
				if saved.Phase == "planned" {
					pass, err = walletPassphrase(true)
					if err != nil {
						return err
					}
				}
				result, err = co.Copy(ctx, pass, cmd.OutOrStdout())
			case "handoff":
				result, err = co.Handoff(ctx)
			case "return":
				result, err = co.Return(ctx)
			}
			if result.Plan.ID != "" {
				migrate.WriteState(cmd.OutOrStdout(), result)
			}
			if err == nil {
				fmt.Fprintln(cmd.OutOrStdout(), migrate.FollowUp)
			}
			return err
		}
		c.AddCommand(step)
	}
	c.AddCommand(&cobra.Command{Use: "_serve", Hidden: true, Args: cobra.NoArgs, Annotations: diagnostic(), RunE: func(cmd *cobra.Command, _ []string) error {
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
		defer stop()
		return migrate.LocalEndpoint().Serve(ctx, cmd.InOrStdin(), cmd.OutOrStdout())
	}})
	c.AddCommand(&cobra.Command{Use: "_receive ID ARGS...", Hidden: true, DisableFlagParsing: true, Args: cobra.MinimumNArgs(2), Annotations: diagnostic(), RunE: func(cmd *cobra.Command, args []string) error {
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
		defer stop()
		return migrate.LocalEndpoint().Receive(ctx, args[0], args[1:], cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr())
	}})
	return c
}

func init() { rootCmd.AddCommand(newMigrateCommand()) }
