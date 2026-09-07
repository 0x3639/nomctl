package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/0x3639/nomctl/internal/node"
	"github.com/0x3639/nomctl/internal/producer"
	"github.com/0x3639/nomctl/internal/service"
	"github.com/0x3639/nomctl/internal/tui"
	"github.com/0x3639/nomctl/internal/ui"
)

var (
	flagPillarPassword     string
	flagPillarYes          bool
	flagPillarShowPassword bool
)

var pillarCmd = &cobra.Command{
	Use:   "pillar",
	Short: "Producer key for running a Pillar on this node",
}

var pillarSetupCmd = &cobra.Command{
	Use:   "setup",
	Short: "Create or configure the producer key and wire it into config.json",
	Long: `Gives the node a producer key, the part of znn-controller's Deploy that
turns a node into a Pillar's producer:

  - a producer configuration already in config.json is kept (asked on a
    terminal; --yes keeps it in scripts);
  - an existing wallet/producer key file is configured after its password
    is verified (prompted, three attempts, or --password);
  - otherwise a new key file is created with a generated 16-character
    password (or --password), printed once.

config.json is backed up as config.json.bak.<unix> before the Producer
section is written, and the node is restarted if it is running. The address
printed at the end is what you set as the Pillar's producer address in
Syrius or znn-cli. One address serves one Pillar.`,
	Args:        cobra.NoArgs,
	Annotations: rootOnly(),
	RunE: func(cmd *cobra.Command, _ []string) error {
		password := flagPillarPassword
		if password == "" {
			// For scripts: keeps the secret out of the command line.
			password = os.Getenv("NOMCTL_PRODUCER_PASSWORD")
		}
		opts := producer.Options{Password: password}
		if ui.Interactive() && !flagPillarYes {
			opts.Prompts = tui.ProducerPrompts()
		}
		var res producer.Result
		err := withLock("pillar", func() error {
			var err error
			res, err = producer.Setup(cfg, opts)
			return err
		})
		if err != nil {
			return err
		}
		printProducerResult(cmd, res)
		return nil
	},
}

func printProducerResult(cmd *cobra.Command, res producer.Result) {
	out := cmd.OutOrStdout()
	fmt.Fprintln(out)
	fmt.Fprintf(out, "%-18s %s\n", "Producer address", res.Address)
	fmt.Fprintf(out, "%-18s %s\n", "Key file", res.KeyFile)
	if res.Password != "" {
		fmt.Fprintf(out, "%-18s %s\n", "Password", res.Password)
		fmt.Fprintln(out, "                   (also stored in config.json, which the node needs; keep a copy elsewhere)")
	}
	if res.ConfigBackup != "" {
		fmt.Fprintf(out, "%-18s %s\n", "Previous config", res.ConfigBackup)
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, producer.NextSteps(res.Address))
}

var pillarDeployCmd = &cobra.Command{
	Use:   "deploy",
	Short: "Deploy a Pillar: build go-zenon master, start the node, create the producer key",
	Long: `The one-step Pillar deployment. Builds the official repository's master
(github.com/zenon-network/go-zenon, whatever NOMCTL_REPO_URL and
NOMCTL_BRANCH_NAME say), installs and starts the go-zenon service, then runs
"pillar setup": an existing producer configuration is kept, an existing key
file is configured after its password is verified, otherwise a new key is
created and its password printed once. Rerunning on a deployed node rebuilds
from master and restarts it.`,
	Args:        cobra.NoArgs,
	Annotations: rootOnly(),
	RunE: func(cmd *cobra.Command, _ []string) error {
		password := flagPillarPassword
		if password == "" {
			password = os.Getenv("NOMCTL_PRODUCER_PASSWORD")
		}
		opts := producer.Options{Password: password}
		if ui.Interactive() && !flagPillarYes {
			opts.Prompts = tui.ProducerPrompts()
		}
		var res producer.Result
		err := withLock("pillar deploy", func() error {
			var err error
			res, err = producer.Deploy(cfg, opts)
			return err
		})
		if err != nil {
			return err
		}
		printProducerResult(cmd, res)
		return nil
	},
}

var pillarStatusCmd = &cobra.Command{
	Use:         "status",
	Short:       "Show the producer configuration and which Pillar uses it",
	Args:        cobra.NoArgs,
	Annotations: diagnostic(),
	RunE: func(cmd *cobra.Command, _ []string) error {
		out := cmd.OutOrStdout()
		pc, err := producer.ReadConfig(producer.ConfigPath(cfg.ZnnDir))
		if err != nil {
			return err
		}
		if pc == nil {
			fmt.Fprintln(out, "No producer configured. Run: sudo nomctl pillar setup")
			return nil
		}
		keyPath := producer.KeyFilePath(cfg.ZnnDir)
		keyState := "present"
		if _, err := os.Stat(keyPath); err != nil {
			keyState = "MISSING"
		} else if addr, err := producer.Address(keyPath); err != nil {
			keyState = "present but UNREADABLE: " + err.Error()
		} else if addr != pc.Address {
			keyState = "present but holds " + addr + ", not the configured address"
		}
		fmt.Fprintf(out, "%-18s %s\n", "Producer address", pc.Address)
		fmt.Fprintf(out, "%-18s %s (%s), index %d\n", "Key file", keyPath, keyState, pc.Index)
		if flagPillarShowPassword {
			fmt.Fprintf(out, "%-18s %s\n", "Password", pc.Password)
		} else {
			fmt.Fprintf(out, "%-18s stored in config.json (--show-password to print)\n", "Password")
		}
		svc := "not running"
		if service.IsActive(cfg.ServiceName) {
			svc = "running"
		}
		fmt.Fprintf(out, "%-18s %s %s\n", "Node", cfg.ServiceName, svc)
		if name := pillarName(); name != "" {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			switch p, err := node.New(node.DefaultURL).PillarByName(ctx, name); {
			case err != nil:
				fmt.Fprintf(out, "%-18s %s: %v\n", "Pillar", name, err)
			case p == nil:
				fmt.Fprintf(out, "%-18s %s not found in the pillar list\n", "Pillar", name)
			case strings.EqualFold(p.ProducerAddress, pc.Address):
				fmt.Fprintf(out, "%-18s %s produces with this address\n", "Pillar", name)
			default:
				fmt.Fprintf(out, "%-18s %s produces with %s, not this node's address; update it in Syrius or znn-cli\n", "Pillar", name, p.ProducerAddress)
			}
		}
		return nil
	},
}

func init() {
	pillarSetupCmd.Flags().StringVar(&flagPillarPassword, "password", "", "password for the key file (generated when creating a new one; verified for an existing one); prefer NOMCTL_PRODUCER_PASSWORD in scripts, which stays out of shell history")
	pillarSetupCmd.Flags().BoolVar(&flagPillarYes, "yes", false, "keep an existing producer configuration without asking")
	pillarStatusCmd.Flags().BoolVar(&flagPillarShowPassword, "show-password", false, "print the key file password from config.json")
	pillarDeployCmd.Flags().StringVar(&flagPillarPassword, "password", "", "password for the producer key (see pillar setup)")
	pillarDeployCmd.Flags().BoolVar(&flagPillarYes, "yes", false, "keep an existing producer configuration without asking")
	pillarCmd.AddCommand(pillarDeployCmd, pillarSetupCmd, pillarStatusCmd)
	rootCmd.AddCommand(pillarCmd)
}
