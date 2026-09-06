// Package analytics ports analytics.sh and grafana.sh: install node_exporter,
// Prometheus and Grafana, wire up the datasources and import the dashboards
// embedded in the binary.
package analytics

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/hypercore-one/nomctl/dashboards"
	"github.com/hypercore-one/nomctl/internal/config"
	"github.com/hypercore-one/nomctl/internal/execx"
	"github.com/hypercore-one/nomctl/internal/fsx"
	"github.com/hypercore-one/nomctl/internal/logx"
	"github.com/hypercore-one/nomctl/internal/service"
	"github.com/hypercore-one/nomctl/internal/ui"
)

const (
	infinityPlugin        = "yesoreyeram-infinity-datasource"
	nodeExporterDashboard = "Node Exporter Full"
	nodeExporterDashURL   = "https://grafana.com/api/dashboards/1860/revisions/latest/download"
	grafanaKeyring        = "/etc/apt/keyrings/grafana.asc"
	grafanaSourcesList    = "/etc/apt/sources.list.d/grafana.list"
	grafanaWait           = 60 * time.Second
)

// Install sets up the full analytics stack.
func Install(cfg config.Config) error {
	ui.Section(os.Stderr, "==== ANALYTICS STACK SETUP ====")
	g := NewGrafana(cfg.GrafanaAdminUser, cfg.GrafanaAdminPassword)

	steps := []struct {
		title string
		fn    func() error
		fatal string
	}{
		{"Installing prerequisites…", installPrerequisites, "failed to install prerequisites"},
		{"Installing Node Exporter…", func() error { return installNodeExporter(cfg) }, "failed to install Node Exporter"},
		{"Installing Prometheus…", func() error { return installPrometheus(cfg) }, "failed to install Prometheus"},
		{"Installing Grafana…", func() error { return installGrafana(g) }, "failed to install Grafana"},
		{"Configuring Grafana datasources…", func() error {
			if err := configurePrometheusDatasource(g); err != nil {
				return err
			}
			if err := installInfinityPlugin(cfg, g); err != nil {
				return err
			}
			return configureInfinityDatasource(g)
		}, "failed to configure datasources/plugins"},
	}
	for _, s := range steps {
		if err := ui.Step(s.title, s.fn); err != nil {
			return fmt.Errorf("%s: %w", s.fatal, err)
		}
	}
	if err := ui.Step("Importing dashboards…", func() error { return importDefaultDashboards(g) }); err != nil {
		slog.Warn("Some dashboards failed to import: " + err.Error())
	}
	logx.Success(fmt.Sprintf("Analytics stack installed successfully. Access Grafana at http://<host>:3000 (%s/%s).", cfg.GrafanaAdminUser, cfg.GrafanaAdminPassword))
	return nil
}

// installPrerequisites installs the apt packages Grafana's repository needs.
func installPrerequisites() error {
	var missing []string
	for _, pkg := range []string{"apt-transport-https", "software-properties-common"} {
		if !dpkgInstalled(pkg) {
			missing = append(missing, pkg)
		}
	}
	if len(missing) == 0 {
		slog.Info("All prerequisite packages already installed.")
		return nil
	}
	slog.Info("Installing missing packages: " + strings.Join(missing, " "))
	if err := execx.Run("apt-get", "update", "-qq"); err != nil {
		return fmt.Errorf("failed to update package lists: %w", err)
	}
	if err := execx.Run("apt-get", append([]string{"install", "-y"}, missing...)...); err != nil {
		return fmt.Errorf("failed to install prerequisite packages: %w", err)
	}
	logx.Success("Prerequisite packages installed.")
	return nil
}

func dpkgInstalled(pkg string) bool {
	out, err := execx.Output("dpkg-query", "-W", "-f=${Status}", pkg)
	return err == nil && strings.Contains(out, "install ok installed")
}

func userExists(name string) bool {
	return execx.New("id", name).Quiet() == nil
}

func ensureSystemUser(name string) error {
	if userExists(name) {
		return nil
	}
	return execx.Run("useradd", "-rs", "/bin/false", name)
}

func releaseArch() string { return "linux-" + runtime.GOARCH }

func installNodeExporter(cfg config.Config) error {
	const unit = "node_exporter"
	if service.IsActive(unit) {
		slog.Info("Node Exporter already running – skipping installation.")
		return nil
	}
	if fsx.Exists("/usr/local/bin/node_exporter") {
		slog.Info("Node Exporter binary exists, starting service...")
		if err := service.DaemonReload(); err != nil {
			return err
		}
		return service.EnableNow(unit)
	}
	slog.Info(fmt.Sprintf("Installing Node Exporter %s…", cfg.NodeExporterVersion))
	dirName := fmt.Sprintf("node_exporter-%s.%s", cfg.NodeExporterVersion, releaseArch())
	url := fmt.Sprintf("https://github.com/prometheus/node_exporter/releases/download/v%s/%s.tar.gz", cfg.NodeExporterVersion, dirName)
	tarball := "/tmp/node_exporter.tar.gz"
	if err := fsx.Download(url, tarball, 5*time.Minute); err != nil {
		return fmt.Errorf("unable to download Node Exporter: %w", err)
	}
	defer func() { _ = os.Remove(tarball); _ = os.RemoveAll(filepath.Join("/tmp", dirName)) }()
	if err := execx.Run("tar", "-xzf", tarball, "-C", "/tmp"); err != nil {
		return err
	}
	if err := fsx.CopyFile(filepath.Join("/tmp", dirName, "node_exporter"), "/usr/local/bin/node_exporter", 0o755); err != nil {
		return err
	}
	if err := ensureSystemUser(unit); err != nil {
		return err
	}
	unitPath := "/etc/systemd/system/node_exporter.service"
	if !fsx.Exists(unitPath) {
		if err := service.WriteUnit(unitPath, NodeExporterUnit()); err != nil {
			return err
		}
	}
	if err := service.DaemonReload(); err != nil {
		return err
	}
	if err := service.EnableNow(unit); err != nil {
		return err
	}
	logx.Success("Node Exporter installed.")
	return nil
}

// NodeExporterUnit renders the node_exporter systemd unit.
func NodeExporterUnit() string {
	return `[Unit]
Description=Prometheus Node Exporter
After=network-online.target

[Service]
User=node_exporter
Group=node_exporter
Type=simple
ExecStart=/usr/local/bin/node_exporter

[Install]
WantedBy=multi-user.target
`
}

// PrometheusUnit renders the prometheus systemd unit.
func PrometheusUnit() string {
	return `[Unit]
Description=Prometheus
After=network-online.target

[Service]
User=prometheus
Group=prometheus
Type=simple
ExecStart=/usr/local/bin/prometheus \
  --config.file=/etc/prometheus/prometheus.yml \
  --storage.tsdb.path=/var/lib/prometheus/

[Install]
WantedBy=multi-user.target
`
}

// nodeScrapeConfig is appended to prometheus.yml when the "node" job is absent.
const nodeScrapeConfig = `
  - job_name: "node"
    static_configs:
      - targets: ["localhost:9100"]
`

// NeedsNodeScrapeJob reports whether prometheus.yml lacks the node job.
func NeedsNodeScrapeJob(promYML string) bool {
	return !strings.Contains(promYML, `job_name: "node"`)
}

func installPrometheus(cfg config.Config) error {
	const unit = "prometheus"
	if service.IsActive(unit) {
		slog.Info("Prometheus already running – skipping installation.")
		return nil
	}
	if fsx.Exists("/usr/local/bin/prometheus") {
		slog.Info("Prometheus binary exists, starting service...")
		if err := service.DaemonReload(); err != nil {
			return err
		}
		return service.EnableNow(unit)
	}
	slog.Info(fmt.Sprintf("Installing Prometheus %s…", cfg.PrometheusVersion))
	for _, d := range []string{"/etc/prometheus", "/var/lib/prometheus"} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	dirName := fmt.Sprintf("prometheus-%s.%s", cfg.PrometheusVersion, releaseArch())
	url := fmt.Sprintf("https://github.com/prometheus/prometheus/releases/download/v%s/%s.tar.gz", cfg.PrometheusVersion, dirName)
	tarball := "/tmp/prometheus.tar.gz"
	if err := fsx.Download(url, tarball, 5*time.Minute); err != nil {
		return fmt.Errorf("unable to download Prometheus: %w", err)
	}
	src := filepath.Join("/tmp", dirName)
	defer func() { _ = os.Remove(tarball); _ = os.RemoveAll(src) }()
	if err := execx.Run("tar", "-xzf", tarball, "-C", "/tmp"); err != nil {
		return err
	}
	for _, bin := range []string{"prometheus", "promtool"} {
		if err := fsx.CopyFile(filepath.Join(src, bin), "/usr/local/bin/"+bin, 0o755); err != nil {
			return err
		}
	}
	for _, dir := range []string{"consoles", "console_libraries"} {
		if !fsx.IsDir("/etc/prometheus/" + dir) {
			if err := execx.Run("cp", "-r", filepath.Join(src, dir), "/etc/prometheus/"); err != nil {
				return err
			}
		}
	}
	const promYML = "/etc/prometheus/prometheus.yml"
	if !fsx.Exists(promYML) {
		if err := fsx.CopyFile(filepath.Join(src, "prometheus.yml"), promYML, 0o644); err != nil {
			return err
		}
	}
	if err := ensureSystemUser(unit); err != nil {
		return err
	}
	unitPath := "/etc/systemd/system/prometheus.service"
	if !fsx.Exists(unitPath) {
		if err := service.WriteUnit(unitPath, PrometheusUnit()); err != nil {
			return err
		}
	}
	if err := execx.Run("chown", "-R", "prometheus:prometheus", "/etc/prometheus", "/var/lib/prometheus"); err != nil {
		return err
	}
	if err := service.DaemonReload(); err != nil {
		return err
	}
	if err := service.EnableNow(unit); err != nil {
		return err
	}

	current, err := os.ReadFile(promYML)
	if err != nil {
		return err
	}
	if NeedsNodeScrapeJob(string(current)) {
		slog.Info("Adding Node Exporter scrape config to Prometheus.")
		f, err := os.OpenFile(promYML, os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		if _, err := f.WriteString(nodeScrapeConfig); err != nil {
			_ = f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		if err := service.RestartUnit(unit); err != nil {
			return err
		}
	}
	logx.Success("Prometheus installed.")
	return nil
}

func installGrafana(g *Grafana) error {
	const unit = "grafana-server"
	if service.IsActive(unit) {
		slog.Info("Grafana already running – skipping installation.")
		return nil
	}
	if dpkgInstalled("grafana") {
		slog.Info("Grafana package already installed, starting service...")
		if err := service.EnableNow(unit); err != nil {
			return err
		}
		return g.WaitReady(grafanaWait)
	}
	slog.Info("Installing Grafana…")
	if err := os.MkdirAll(filepath.Dir(grafanaKeyring), 0o755); err != nil {
		return err
	}
	if !fsx.Exists(grafanaKeyring) {
		if err := fsx.Download("https://apt.grafana.com/gpg.key", grafanaKeyring, time.Minute); err != nil {
			return err
		}
	}
	if !fsx.Exists(grafanaSourcesList) {
		line := fmt.Sprintf("deb [signed-by=%s] https://apt.grafana.com stable main\n", grafanaKeyring)
		if err := os.WriteFile(grafanaSourcesList, []byte(line), 0o644); err != nil {
			return err
		}
		if err := execx.Run("apt-get", "update", "-qq"); err != nil {
			return err
		}
	}
	if err := execx.Run("apt-get", "install", "-y", "grafana"); err != nil {
		return fmt.Errorf("failed to install Grafana: %w", err)
	}
	if err := service.EnableNow(unit); err != nil {
		return err
	}
	if err := g.WaitReady(grafanaWait); err != nil {
		return err
	}
	logx.Success("Grafana installed.")
	return nil
}

func configurePrometheusDatasource(g *Grafana) error {
	slog.Info("Configuring Prometheus datasource in Grafana…")
	exists, err := g.DatasourceExists("Prometheus")
	if err != nil {
		return err
	}
	if exists {
		slog.Info("Prometheus datasource already exists.")
		return nil
	}
	if err := g.CreateDatasource(map[string]any{
		"name": "Prometheus", "type": "prometheus", "url": "http://localhost:9090", "access": "proxy", "isDefault": true,
	}); err != nil {
		return fmt.Errorf("failed to configure Prometheus datasource: %w", err)
	}
	logx.Success("Prometheus datasource configured.")
	return nil
}

func installInfinityPlugin(cfg config.Config, g *Grafana) error {
	out, err := execx.Output("grafana-cli", "plugins", "ls")
	if err == nil && strings.Contains(out, infinityPlugin) {
		slog.Info("Infinity plugin already installed.")
		return nil
	}
	slog.Info("Installing Infinity datasource plugin…")
	if err := execx.Run("grafana-cli", "plugins", "install", infinityPlugin, cfg.InfinityPluginVersion); err != nil {
		return fmt.Errorf("failed to install Infinity plugin: %w", err)
	}
	if err := execx.Run("chown", "-R", "grafana:grafana", "/var/lib/grafana/plugins"); err != nil {
		return err
	}
	if err := service.RestartUnit("grafana-server"); err != nil {
		return err
	}
	if err := g.WaitReady(grafanaWait); err != nil {
		return err
	}
	logx.Success("Infinity plugin installed.")
	return nil
}

func configureInfinityDatasource(g *Grafana) error {
	slog.Info("Configuring Infinity datasource in Grafana…")
	exists, err := g.DatasourceExists(infinityPlugin)
	if err != nil {
		return err
	}
	if exists {
		slog.Info("Infinity datasource already exists.")
		return nil
	}
	if err := g.CreateDatasource(map[string]any{
		"name": infinityPlugin, "type": infinityPlugin, "access": "proxy",
	}); err != nil {
		return fmt.Errorf("failed to configure Infinity datasource: %w", err)
	}
	logx.Success("Infinity datasource configured.")
	return nil
}

// importDefaultDashboards imports "Node Exporter Full" from grafana.com and
// the embedded znnd dashboard. Failures are collected, not fatal.
func importDefaultDashboards(g *Grafana) error {
	var problems []string

	exists, err := g.DashboardExists(nodeExporterDashboard)
	switch {
	case err != nil:
		problems = append(problems, err.Error())
	case exists:
		slog.Info("Node Exporter dashboard already exists – skipping.")
	default:
		tmp := "/tmp/node_exporter_dashboard.json"
		if err := fsx.Download(nodeExporterDashURL, tmp, time.Minute); err != nil {
			problems = append(problems, "failed to download Node Exporter dashboard")
		} else {
			data, err := os.ReadFile(tmp)
			_ = os.Remove(tmp)
			if err == nil {
				slog.Info("Importing dashboard " + nodeExporterDashboard + "…")
				err = importDB(g, data)
			}
			if err != nil {
				problems = append(problems, "failed to import Node Exporter dashboard: "+err.Error())
			} else {
				logx.Success("Dashboard imported successfully.")
			}
		}
	}

	data, err := dashboards.FS.ReadFile(dashboards.Node)
	if err != nil {
		return fmt.Errorf("embedded dashboard: %w", err)
	}
	title := DashboardTitle(data)
	exists, err = g.DashboardExists(title)
	switch {
	case err != nil:
		problems = append(problems, err.Error())
	case exists:
		slog.Info(fmt.Sprintf("Dashboard '%s' already exists – skipping import.", title))
	default:
		slog.Info("Importing embedded " + dashboards.Node + " dashboard…")
		payload, err := ImportPayload(data)
		if err == nil {
			err = g.ImportDashboard(payload)
		}
		if err != nil {
			problems = append(problems, "failed to import "+dashboards.Node+": "+err.Error())
		} else {
			logx.Success("ZNND dashboard imported successfully.")
		}
	}

	if len(problems) > 0 {
		return fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	return nil
}

func importDB(g *Grafana, dashboard []byte) error {
	payload, err := DBPayload(dashboard)
	if err != nil {
		return err
	}
	return g.PostDashboard(payload)
}
