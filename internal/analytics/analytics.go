// Package analytics ports analytics.sh and grafana.sh: install node_exporter,
// Prometheus and Grafana, wire up the datasources and import the dashboards
// embedded in the binary.
package analytics

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/0x3639/nomctl/dashboards"
	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/execx"
	"github.com/0x3639/nomctl/internal/fsx"
	"github.com/0x3639/nomctl/internal/logx"
	"github.com/0x3639/nomctl/internal/service"
	"github.com/0x3639/nomctl/internal/ui"
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
	g.BaseURL = ClientURL(cfg.GrafanaHTTPAddr)

	steps := []struct {
		title string
		fn    func() error
		fatal string
	}{
		{"Installing prerequisites…", installPrerequisites, "failed to install prerequisites"},
		{"Installing Node Exporter…", func() error { return installNodeExporter(cfg) }, "failed to install Node Exporter"},
		{"Installing Prometheus…", func() error { return installPrometheus(cfg) }, "failed to install Prometheus"},
		{"Installing Grafana…", func() error { return installGrafana(cfg, g) }, "failed to install Grafana"},
		{"Securing Grafana…", func() error { return secureGrafana(cfg, g) }, "failed to secure Grafana"},
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
	access := fmt.Sprintf("Analytics stack installed successfully. Grafana listens on %s:3000 as %s", cfg.GrafanaHTTPAddr, cfg.GrafanaAdminUser)
	if cfg.GrafanaHTTPAddr == config.DefaultGrafanaHTTPAddr {
		access += "; reach it with: ssh -L 3000:127.0.0.1:3000 root@<host>"
	}
	logx.Success(access + ".")
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

// ensureUnitFile also migrates a known previous generated unit, while keeping
// operator-managed units intact.
func ensureUnitFile(path, content string, previous ...string) (bool, error) {
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return false, fmt.Errorf("%s must be a regular unit file", path)
		}
		current, err := readNoFollow(path)
		if err != nil {
			return false, err
		}
		if string(current) == content {
			return false, nil
		}
		managed := false
		for _, old := range previous {
			managed = managed || string(current) == old
		}
		if !managed {
			slog.Warn("Keeping existing custom unit; verify its listen address", "unit", path)
			return false, nil
		}
	} else if !os.IsNotExist(err) {
		return false, err
	}
	slog.Info("Writing " + path)
	candidate, err := managedCandidate(path, []byte(content), 0o644)
	if err != nil {
		return false, err
	}
	defer func() { _ = os.Remove(candidate) }()
	return true, os.Rename(candidate, path)
}

// installNodeExporter converges the node_exporter installation: every step
// checks its own precondition, so an interrupted earlier run is completed
// rather than skipped.
func installNodeExporter(cfg config.Config) error {
	const unit = "node_exporter"
	const binary = "/usr/local/bin/node_exporter"

	if !fsx.Exists(binary) {
		slog.Info(fmt.Sprintf("Installing Node Exporter %s…", cfg.NodeExporterVersion))
		dirName := fmt.Sprintf("node_exporter-%s.%s", cfg.NodeExporterVersion, releaseArch())
		url := fmt.Sprintf("https://github.com/prometheus/node_exporter/releases/download/v%s/%s.tar.gz", cfg.NodeExporterVersion, dirName)
		work, err := os.MkdirTemp("", "nomctl-node-exporter-") // private: nothing else can plant files here
		if err != nil {
			return err
		}
		defer func() { _ = os.RemoveAll(work) }()
		tarball := filepath.Join(work, "node_exporter.tar.gz")
		if err := fsx.Download(url, tarball, 5*time.Minute); err != nil {
			return fmt.Errorf("unable to download Node Exporter: %w", err)
		}
		if err := execx.Run("tar", "-xzf", tarball, "-C", work); err != nil {
			return err
		}
		if err := fsx.CopyFile(filepath.Join(work, dirName, "node_exporter"), binary, 0o755); err != nil {
			return err
		}
	} else {
		slog.Info("Node Exporter binary already present.")
	}
	if err := ensureSystemUser(unit); err != nil {
		return err
	}
	want := NodeExporterUnit()
	previous := strings.Replace(want, " --web.listen-address=127.0.0.1:9100", "", 1)
	_, err := ensureUnitFile("/etc/systemd/system/node_exporter.service", want, previous)
	if err != nil {
		return err
	}
	wasActive := service.IsActive(unit)
	if err := activateMonitoringService(unit, wasActive); err != nil {
		return err
	}
	logx.Success("Node Exporter installed and running.")
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
ExecStart=/usr/local/bin/node_exporter --web.listen-address=127.0.0.1:9100

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
  --web.listen-address=127.0.0.1:9090 \
  --config.file=/etc/prometheus/prometheus.yml \
  --storage.tsdb.path=/var/lib/prometheus/

[Install]
WantedBy=multi-user.target
`
}

// AppendManaged appends text to a root-managed configuration file without
// following links: the target must be a regular file, and the new content
// is written to a private sibling and renamed over it.
func AppendManaged(path, text string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file (mode %s); refusing to write through it", path, info.Mode())
	}
	current, err := readNoFollow(path)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = tmp.Close(); _ = os.Remove(tmpPath) }
	if _, err := tmp.Write(append(current, text...)); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Chmod(info.Mode().Perm()); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}

// readNoFollow reads a file opened with O_NOFOLLOW where the platform has
// it, so a link planted between Lstat and open is still refused.
func readNoFollow(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|noFollow, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(f)
}

// installPrometheus converges the Prometheus installation (see
// installNodeExporter for the approach).
func installPrometheus(cfg config.Config) error {
	const unit = "prometheus"
	const promYML = "/etc/prometheus/prometheus.yml"
	for _, d := range []string{"/etc/prometheus", "/var/lib/prometheus"} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}

	// Pieces from the release tarball, and where each lands.
	binaries := []string{"prometheus", "promtool"}
	dirs := []string{"consoles", "console_libraries"}
	missing := false
	for _, b := range binaries {
		missing = missing || !fsx.Exists("/usr/local/bin/"+b)
	}
	for _, d := range dirs {
		missing = missing || !fsx.IsDir("/etc/prometheus/"+d)
	}
	missing = missing || !fsx.Exists(promYML)

	if missing {
		slog.Info(fmt.Sprintf("Installing Prometheus %s…", cfg.PrometheusVersion))
		dirName := fmt.Sprintf("prometheus-%s.%s", cfg.PrometheusVersion, releaseArch())
		url := fmt.Sprintf("https://github.com/prometheus/prometheus/releases/download/v%s/%s.tar.gz", cfg.PrometheusVersion, dirName)
		work, err := os.MkdirTemp("", "nomctl-prometheus-")
		if err != nil {
			return err
		}
		defer func() { _ = os.RemoveAll(work) }()
		tarball := filepath.Join(work, "prometheus.tar.gz")
		if err := fsx.Download(url, tarball, 5*time.Minute); err != nil {
			return fmt.Errorf("unable to download Prometheus: %w", err)
		}
		src := filepath.Join(work, dirName)
		if err := execx.Run("tar", "-xzf", tarball, "-C", work); err != nil {
			return err
		}
		for _, b := range binaries {
			if !fsx.Exists("/usr/local/bin/" + b) {
				if err := fsx.CopyFile(filepath.Join(src, b), "/usr/local/bin/"+b, 0o755); err != nil {
					return err
				}
			}
		}
		for _, d := range dirs {
			if !fsx.IsDir("/etc/prometheus/" + d) {
				if err := execx.Run("cp", "-r", filepath.Join(src, d), "/etc/prometheus/"); err != nil {
					return err
				}
			}
		}
		if !fsx.Exists(promYML) {
			if err := fsx.CopyFile(filepath.Join(src, "prometheus.yml"), promYML, 0o644); err != nil {
				return err
			}
		}
	} else {
		slog.Info("Prometheus files already present.")
	}

	if err := ensureSystemUser(unit); err != nil {
		return err
	}
	want := PrometheusUnit()
	previous := strings.Replace(want, "  --web.listen-address=127.0.0.1:9090 \\\n", "", 1)
	_, err := ensureUnitFile("/etc/systemd/system/prometheus.service", want, previous)
	if err != nil {
		return err
	}
	// Configuration stays root-owned (world-readable) so the service
	// account cannot swap a managed file for a symlink before a later root
	// run; only the data directory belongs to the service.
	if err := execx.Run("chown", "-R", "root:root", "/etc/prometheus"); err != nil {
		return err
	}
	if err := execx.Run("chown", "-R", "prometheus:prometheus", "/var/lib/prometheus"); err != nil {
		return err
	}
	wasActive := service.IsActive(unit)
	var recoverService func() error
	if wasActive {
		recoverService = func() error { return service.RestartUnit(unit) }
	}
	validate := func(path string) error { return execx.Run("/usr/local/bin/promtool", "check", "config", path) }
	// Reapply after validation even on retries where the unit already matches.
	activate := func(_ bool) error { return activateMonitoringService(unit, wasActive) }
	if err := configurePrometheus(promYML, validate, activate, recoverService); err != nil {
		return err
	}
	logx.Success("Prometheus installed and running.")
	return nil
}

// grafanaDropIn is the systemd drop-in that binds Grafana to the configured
// address. Grafana reads GF_SERVER_HTTP_ADDR, so no grafana.ini edit is needed.
const grafanaDropIn = "/etc/systemd/system/grafana-server.service.d/nomctl.conf"

// GrafanaDropIn renders the bind-address drop-in.
func GrafanaDropIn(httpAddr string) string {
	return fmt.Sprintf("[Service]\nEnvironment=GF_SERVER_HTTP_ADDR=%s\n", httpAddr)
}

// installGrafana converges the Grafana package install and service state.
func installGrafana(cfg config.Config, g *Grafana) error {
	const unit = "grafana-server"
	if !dpkgInstalled("grafana") {
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
		}
		// Always refresh: the sources file may exist from an earlier run
		// that never got as far as apt-get update.
		if err := execx.Run("apt-get", "update", "-qq"); err != nil {
			return err
		}
		if err := execx.Run("apt-get", "install", "-y", "grafana"); err != nil {
			return fmt.Errorf("failed to install Grafana: %w", err)
		}
	} else {
		slog.Info("Grafana package already installed.")
	}
	// Bind address via drop-in; a change restarts Grafana.
	want := GrafanaDropIn(cfg.GrafanaHTTPAddr)
	current, _ := os.ReadFile(grafanaDropIn)
	reload := false
	if string(current) != want {
		if err := os.MkdirAll(filepath.Dir(grafanaDropIn), 0o755); err != nil {
			return err
		}
		if err := service.WriteUnit(grafanaDropIn, want); err != nil {
			return err
		}
		reload = true
	}
	if err := service.EnsureRunning(unit, reload); err != nil {
		return err
	}
	if reload && service.IsActive(unit) {
		if err := service.RestartUnit(unit); err != nil {
			return err
		}
	}
	if err := g.WaitReady(grafanaWait); err != nil {
		return err
	}
	logx.Success(fmt.Sprintf("Grafana installed and running on %s:3000.", cfg.GrafanaHTTPAddr))
	return nil
}

// secureGrafana applies the configured admin password. Grafana starts with
// admin/admin; if the configured password differs and the configured one is
// rejected while the default still works, the password is changed.
func secureGrafana(cfg config.Config, g *Grafana) error {
	if cfg.GrafanaAdminPassword == config.DefaultGrafanaAdminPassword {
		slog.Warn("Grafana is using the default admin password; set NOMCTL_GRAFANA_ADMIN_PASSWORD and rerun analytics install to change it")
		return nil
	}
	if ok, _ := g.Authenticated(); ok {
		slog.Info("Grafana admin password already applied.")
		return nil
	}
	initial := NewGrafana(cfg.GrafanaAdminUser, config.DefaultGrafanaAdminPassword)
	initial.BaseURL = g.BaseURL
	initial.Client = g.Client
	if ok, err := initial.Authenticated(); err != nil || !ok {
		return fmt.Errorf("neither the configured Grafana password nor the default works; set NOMCTL_GRAFANA_ADMIN_PASSWORD to the current password")
	}
	if err := initial.ChangePassword(config.DefaultGrafanaAdminPassword, cfg.GrafanaAdminPassword); err != nil {
		return err
	}
	logx.Success("Grafana admin password set from NOMCTL_GRAFANA_ADMIN_PASSWORD.")
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

// installInfinityPlugin installs the plugin if grafana-cli does not list it,
// fixes ownership, and restarts Grafana whenever the running instance has
// not loaded the plugin yet (e.g. an earlier run installed it but was
// interrupted before the restart).
func installInfinityPlugin(cfg config.Config, g *Grafana) error {
	out, err := execx.Output("grafana-cli", "plugins", "ls")
	if err != nil || !strings.Contains(out, infinityPlugin) {
		slog.Info("Installing Infinity datasource plugin…")
		if err := execx.Run("grafana-cli", "plugins", "install", infinityPlugin, cfg.InfinityPluginVersion); err != nil {
			return fmt.Errorf("failed to install Infinity plugin: %w", err)
		}
	} else {
		slog.Info("Infinity plugin already installed.")
	}
	if fsx.IsDir("/var/lib/grafana/plugins") {
		if err := execx.Run("chown", "-R", "grafana:grafana", "/var/lib/grafana/plugins"); err != nil {
			return err
		}
	}
	loaded, err := g.PluginLoaded(infinityPlugin)
	if err != nil {
		return err
	}
	if !loaded {
		slog.Info("Restarting Grafana to load the Infinity plugin…")
		if err := service.RestartUnit("grafana-server"); err != nil {
			return err
		}
		if err := g.WaitReady(grafanaWait); err != nil {
			return err
		}
		if loaded, err = g.PluginLoaded(infinityPlugin); err != nil {
			return err
		}
		if !loaded {
			return fmt.Errorf("Grafana did not load plugin %s after restart", infinityPlugin)
		}
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
		uid, err := g.DatasourceUID(infinityPlugin)
		if err == nil && uid == "" {
			err = fmt.Errorf("datasource %s not found", infinityPlugin)
		}
		var payload []byte
		if err == nil {
			payload, err = ImportPayload(data, uid)
		}
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

// activateMonitoringService reapplies the on-disk unit on every setup run.
// A previous installation may have stopped after writing it but before
// reloading or restarting. A newly started service is not restarted twice.
func activateMonitoringService(unit string, wasActive bool) error {
	if err := service.EnsureRunning(unit, true); err != nil {
		return err
	}
	if wasActive {
		return service.RestartUnit(unit)
	}
	return nil
}
