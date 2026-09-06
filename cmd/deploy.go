package cmd

import (
	"github.com/spf13/cobra"

	"github.com/hypercore-one/nomctl/internal/deploy"
)

var (
	flagDeployRepo   string
	flagDeployBranch string
)

var deployCmd = &cobra.Command{
	Use:   "deploy",
	Short: "Build the node from source and set up its systemd service",
	Long: `Installs build dependencies and the Go toolchain, clones the repository,
builds the node binary, installs it, creates and enables the systemd unit
and starts the service.`,
	Args:        cobra.NoArgs,
	Annotations: rootOnly(),
	RunE: func(*cobra.Command, []string) error {
		return deploy.Run(cfg, flagDeployRepo, flagDeployBranch)
	},
}

func init() {
	deployCmd.Flags().StringVar(&flagDeployRepo, "repo", "", "git repository URL (NOMCTL_REPO_URL)")
	deployCmd.Flags().StringVar(&flagDeployBranch, "branch", "", "git branch name (NOMCTL_BRANCH_NAME)")
	rootCmd.AddCommand(deployCmd)
}
