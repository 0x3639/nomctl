package cmd

import (
	"github.com/spf13/cobra"

	"github.com/hypercore-one/nomctl/internal/analytics"
)

var analyticsCmd = &cobra.Command{
	Use:   "analytics",
	Short: "Manage the Prometheus + Grafana analytics stack",
}

var analyticsInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Install node_exporter, Prometheus and Grafana with the node dashboards",
	Long: `Installs Prometheus node_exporter and Prometheus from GitHub releases,
Grafana from its apt repository, configures the Prometheus and Infinity
datasources and imports the "Node Exporter Full" dashboard plus the znnd
dashboard embedded in this binary. Grafana listens on port 3000.`,
	Args:        cobra.NoArgs,
	Annotations: rootOnly(),
	RunE:        func(*cobra.Command, []string) error { return analytics.Install(cfg) },
}

func init() {
	analyticsCmd.AddCommand(analyticsInstallCmd)
	rootCmd.AddCommand(analyticsCmd)
}
