// Package config centralises every tunable used by nomctl.
//
// Values are read from NOMCTL_* environment variables (the successor of the
// ZNNSH_* variables used by the original bash toolkit). Command-line flags are
// applied on top by the cobra commands, so precedence is: flag > env > default.
package config

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
)

// EnvPrefix is the prefix shared by every environment variable nomctl reads.
const EnvPrefix = "NOMCTL_"

// Defaults mirrored from lib/config.sh of hypercore-one/deployment.
const (
	DefaultRepoURL     = "https://github.com/zenon-network/go-zenon.git"
	DefaultBranchName  = "master"
	DefaultBinaryName  = "znnd"
	DefaultServiceName = "go-zenon"
	DefaultGoVersion   = "1.23.0"

	DefaultInstallDir = "/usr/local/bin"
	DefaultZnnDir     = "/root/.znn"
	DefaultWorkDir    = "/opt/nomctl"
	DefaultLogFile    = "/var/log/nomctl.log"

	DefaultBackupDir         = "/backup"
	DefaultMaxBackups        = 7
	DefaultBackupCadenceDays = 0
	DefaultMinFreeSpaceKB    = 15728640 // 15 GB

	DefaultNodeExporterVersion   = "1.6.1"
	DefaultPrometheusVersion     = "2.47.0"
	DefaultInfinityPluginVersion = "2.10.0"
	DefaultGrafanaAdminUser      = "admin"
	DefaultGrafanaAdminPassword  = "admin"
	DefaultGrafanaHTTPAddr       = "127.0.0.1"
	DefaultReleaseRepo           = "0x3639/nomctl"
	DefaultBootstrapURL          = "https://hypercore.nyc3.digitaloceanspaces.com/bootstrap/2026-09-01/bootstrap-20260901010001.zip"
)

// Config holds every setting nomctl needs at runtime.
type Config struct {
	// Debug enables verbose logging and streams external command output to
	// the terminal instead of the log file. Env: NOMCTL_DEBUG.
	Debug bool
	// LogFile receives a plain-text copy of every log line plus the output of
	// external commands. Env: NOMCTL_LOG_FILE.
	LogFile string
	// SkipPreflight disables the CPU/RAM/NTP/Internet checks that run before
	// every privileged command. Env: NOMCTL_SKIP_PREFLIGHT.
	SkipPreflight bool

	// WorkDir is where the Go toolchain and the go-zenon checkout live.
	// Env: NOMCTL_WORK_DIR.
	WorkDir string
	// InstallDir is where the built node binary is copied. Env: NOMCTL_INSTALL_DIR.
	InstallDir string
	// ZnnDir is the node data directory. Env: NOMCTL_ZNN_DIR.
	ZnnDir string

	// RepoURL / BranchName select what `nomctl deploy` builds.
	// Env: NOMCTL_REPO_URL, NOMCTL_BRANCH_NAME.
	RepoURL    string
	BranchName string
	// BinaryName is the name of the node executable. Env: NOMCTL_BINARY_NAME.
	BinaryName string
	// ServiceName is the systemd unit (without .service). Env: NOMCTL_SERVICE_NAME.
	ServiceName string
	// GoVersion is the Go toolchain used to build the node. Env: NOMCTL_GO_VERSION.
	GoVersion string
	// ReleaseRepo is the GitHub repository nomctl upgrades itself from.
	// Env: NOMCTL_REPO.
	ReleaseRepo string
	// UpdateCheck enables the "update available" check in status/top.
	// Env: NOMCTL_UPDATE_CHECK.
	UpdateCheck bool
	// PillarName makes status/top show this pillar's production. The alerts
	// config, when present, takes precedence. Env: NOMCTL_PILLAR_NAME.
	PillarName string
	// BootstrapURL is the default snapshot for `nomctl bootstrap`.
	// Env: NOMCTL_BOOTSTRAP_URL.
	BootstrapURL string

	// Backup settings. Env: NOMCTL_BACKUP_DIR, NOMCTL_MAX_BACKUPS,
	// NOMCTL_BACKUP_CADENCE_DAYS, NOMCTL_BACKUP_HOUR, NOMCTL_MIN_FREE_SPACE_KB.
	BackupDir         string
	MaxBackups        int
	BackupCadenceDays int
	// BackupHour is the hour (0-23) for scheduled backups. -1 means "unset",
	// in which case a deterministic hour between 02:00 and 04:59 is derived
	// from the hostname.
	BackupHour     int
	MinFreeSpaceKB int64

	// Analytics stack settings. Env: NOMCTL_NODE_EXPORTER_VERSION,
	// NOMCTL_PROMETHEUS_VERSION, NOMCTL_INFINITY_PLUGIN_VERSION,
	// NOMCTL_GRAFANA_ADMIN_USER, NOMCTL_GRAFANA_ADMIN_PASSWORD.
	NodeExporterVersion   string
	PrometheusVersion     string
	InfinityPluginVersion string
	GrafanaAdminUser      string
	GrafanaAdminPassword  string
	// GrafanaHTTPAddr is the address Grafana binds to; 127.0.0.1 keeps it
	// reachable only through an SSH tunnel or a local reverse proxy.
	// Env: NOMCTL_GRAFANA_HTTP_ADDR.
	GrafanaHTTPAddr string
}

// Default returns a Config populated with built-in defaults only.
func Default() Config {
	return Config{
		LogFile:               DefaultLogFile,
		WorkDir:               DefaultWorkDir,
		InstallDir:            DefaultInstallDir,
		ZnnDir:                DefaultZnnDir,
		RepoURL:               DefaultRepoURL,
		BranchName:            DefaultBranchName,
		BinaryName:            DefaultBinaryName,
		ServiceName:           DefaultServiceName,
		GoVersion:             DefaultGoVersion,
		BackupDir:             DefaultBackupDir,
		MaxBackups:            DefaultMaxBackups,
		BackupCadenceDays:     DefaultBackupCadenceDays,
		BackupHour:            -1,
		MinFreeSpaceKB:        DefaultMinFreeSpaceKB,
		NodeExporterVersion:   DefaultNodeExporterVersion,
		PrometheusVersion:     DefaultPrometheusVersion,
		InfinityPluginVersion: DefaultInfinityPluginVersion,
		GrafanaAdminUser:      DefaultGrafanaAdminUser,
		GrafanaAdminPassword:  DefaultGrafanaAdminPassword,
		GrafanaHTTPAddr:       DefaultGrafanaHTTPAddr,
		ReleaseRepo:           DefaultReleaseRepo,
		BootstrapURL:          DefaultBootstrapURL,
		UpdateCheck:           true,
	}
}

// Load builds a Config from defaults overridden by the environment.
func Load() (Config, error) {
	return LoadFrom(os.LookupEnv)
}

// LoadFrom is Load with an injectable environment lookup, for tests.
func LoadFrom(lookup func(string) (string, bool)) (Config, error) {
	c := Default()
	var errs []string

	str := func(key string, dst *string) {
		if v, ok := lookup(EnvPrefix + key); ok && strings.TrimSpace(v) != "" {
			*dst = strings.TrimSpace(v)
		}
	}
	boolean := func(key string, dst *bool) {
		v, ok := lookup(EnvPrefix + key)
		if !ok || strings.TrimSpace(v) == "" {
			return
		}
		b, err := strconv.ParseBool(strings.TrimSpace(v))
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s%s: %q is not a boolean", EnvPrefix, key, v))
			return
		}
		*dst = b
	}
	integer := func(key string, dst *int) {
		v, ok := lookup(EnvPrefix + key)
		if !ok || strings.TrimSpace(v) == "" {
			return
		}
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s%s: %q is not an integer", EnvPrefix, key, v))
			return
		}
		*dst = n
	}
	integer64 := func(key string, dst *int64) {
		v, ok := lookup(EnvPrefix + key)
		if !ok || strings.TrimSpace(v) == "" {
			return
		}
		n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s%s: %q is not an integer", EnvPrefix, key, v))
			return
		}
		*dst = n
	}

	boolean("DEBUG", &c.Debug)
	str("LOG_FILE", &c.LogFile)
	boolean("SKIP_PREFLIGHT", &c.SkipPreflight)
	str("WORK_DIR", &c.WorkDir)
	str("INSTALL_DIR", &c.InstallDir)
	str("ZNN_DIR", &c.ZnnDir)
	str("REPO_URL", &c.RepoURL)
	str("BRANCH_NAME", &c.BranchName)
	str("BINARY_NAME", &c.BinaryName)
	str("SERVICE_NAME", &c.ServiceName)
	str("GO_VERSION", &c.GoVersion)
	str("PILLAR_NAME", &c.PillarName)
	str("REPO", &c.ReleaseRepo)
	boolean("UPDATE_CHECK", &c.UpdateCheck)
	str("BOOTSTRAP_URL", &c.BootstrapURL)
	str("BACKUP_DIR", &c.BackupDir)
	integer("MAX_BACKUPS", &c.MaxBackups)
	integer("BACKUP_CADENCE_DAYS", &c.BackupCadenceDays)
	integer("BACKUP_HOUR", &c.BackupHour)
	integer64("MIN_FREE_SPACE_KB", &c.MinFreeSpaceKB)
	str("NODE_EXPORTER_VERSION", &c.NodeExporterVersion)
	str("PROMETHEUS_VERSION", &c.PrometheusVersion)
	str("INFINITY_PLUGIN_VERSION", &c.InfinityPluginVersion)
	str("GRAFANA_ADMIN_USER", &c.GrafanaAdminUser)
	str("GRAFANA_ADMIN_PASSWORD", &c.GrafanaAdminPassword)
	str("GRAFANA_HTTP_ADDR", &c.GrafanaHTTPAddr)

	if len(errs) > 0 {
		return c, fmt.Errorf("invalid configuration: %s", strings.Join(errs, "; "))
	}
	return c, nil
}

// Redacted returns a printable copy with secrets masked, for debug logs.
func (c Config) Redacted() string {
	if c.GrafanaAdminPassword != "" {
		c.GrafanaAdminPassword = "***"
	}
	return fmt.Sprintf("%+v", c)
}

// Validate checks value ranges. Load does not call it, so that flags can
// override an invalid environment value; commands call it once flags are
// applied.
func (c Config) Validate() error {
	if c.MaxBackups < 1 {
		return fmt.Errorf("max backups must be at least 1 (got %d)", c.MaxBackups)
	}
	if c.BackupCadenceDays < 0 {
		return fmt.Errorf("backup cadence must not be negative (got %d)", c.BackupCadenceDays)
	}
	if c.BackupHour != -1 && (c.BackupHour < 0 || c.BackupHour > 23) {
		return fmt.Errorf("backup hour must be between 0 and 23 (got %d)", c.BackupHour)
	}
	if c.MinFreeSpaceKB < 0 {
		return fmt.Errorf("min free space must not be negative (got %d)", c.MinFreeSpaceKB)
	}
	return nil
}

// GoArch maps the running architecture onto the Go download naming scheme.
// Only linux/amd64 and linux/arm64 are supported.
func GoArch() (string, error) {
	switch runtime.GOARCH {
	case "amd64", "arm64":
		return runtime.GOARCH, nil
	default:
		return "", fmt.Errorf("%s architecture is not supported", runtime.GOARCH)
	}
}

// GoURL returns the download URL of the configured Go toolchain for this host.
func (c Config) GoURL() (string, error) {
	arch, err := GoArch()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("https://go.dev/dl/go%s.linux-%s.tar.gz", c.GoVersion, arch), nil
}

// GoRoot is the directory the Go toolchain is unpacked into.
func (c Config) GoRoot() string { return c.WorkDir + "/go" }

// GoBinary is the path to the go executable used for building the node.
func (c Config) GoBinary() string { return c.GoRoot() + "/bin/go" }

// SourceDir is the directory the node repository is cloned into.
func (c Config) SourceDir() string { return c.WorkDir + "/" + c.ServiceName }

// ServiceUnit is the full systemd unit name.
func (c Config) ServiceUnit() string { return c.ServiceName + ".service" }

// ServiceUnitPath is the path of the systemd unit file nomctl manages.
func (c Config) ServiceUnitPath() string { return "/etc/systemd/system/" + c.ServiceUnit() }

// BinaryPath is where the node binary is installed.
func (c Config) BinaryPath() string { return c.InstallDir + "/" + c.BinaryName }

// Var describes one environment variable for documentation purposes.
type Var struct {
	Name, Default, Description string
}

// Vars lists every environment variable nomctl understands, with defaults.
func Vars() []Var {
	return []Var{
		{"NOMCTL_DEBUG", "false", "Verbose logging; stream external command output to the terminal"},
		{"NOMCTL_LOG_FILE", DefaultLogFile, "Plain-text log file (also receives external command output)"},
		{"NOMCTL_SKIP_PREFLIGHT", "false", "Skip the CPU/RAM/NTP/Internet pre-flight checks"},
		{"NOMCTL_WORK_DIR", DefaultWorkDir, "Where the Go toolchain and source checkout are kept"},
		{"NOMCTL_INSTALL_DIR", DefaultInstallDir, "Where the node binary is installed"},
		{"NOMCTL_ZNN_DIR", DefaultZnnDir, "Node data directory"},
		{"NOMCTL_REPO_URL", DefaultRepoURL, "Git repository to build"},
		{"NOMCTL_BRANCH_NAME", DefaultBranchName, "Git branch to build"},
		{"NOMCTL_BINARY_NAME", DefaultBinaryName, "Node binary name (also the cmd/ package built)"},
		{"NOMCTL_SERVICE_NAME", DefaultServiceName, "systemd service name"},
		{"NOMCTL_GO_VERSION", DefaultGoVersion, "Go toolchain version used to build the node"},
		{"NOMCTL_PILLAR_NAME", "(unset)", "Pillar name for status/top production stats (alerts config takes precedence)"},
		{"NOMCTL_REPO", DefaultReleaseRepo, "GitHub repository nomctl upgrade downloads releases from"},
		{"NOMCTL_UPDATE_CHECK", "true", "Check GitHub for newer nomctl and go-zenon in status/top (cached 6h)"},
		{"NOMCTL_BOOTSTRAP_URL", DefaultBootstrapURL, "Snapshot for nomctl bootstrap (.zip with a .hash sidecar next to it)"},
		{"NOMCTL_BACKUP_DIR", DefaultBackupDir, "Directory that stores backup archives"},
		{"NOMCTL_MAX_BACKUPS", strconv.Itoa(DefaultMaxBackups), "Number of backups to retain"},
		{"NOMCTL_BACKUP_CADENCE_DAYS", strconv.Itoa(DefaultBackupCadenceDays), "Days between scheduled backups (0 = every run)"},
		{"NOMCTL_BACKUP_HOUR", "(unset)", "Hour (0-23) for scheduled backups; unset = derived, between 02:00 and 04:59"},
		{"NOMCTL_MIN_FREE_SPACE_KB", strconv.Itoa(DefaultMinFreeSpaceKB), "Minimum free space in the backup directory (15 GB)"},
		{"NOMCTL_NODE_EXPORTER_VERSION", DefaultNodeExporterVersion, "Prometheus node_exporter version"},
		{"NOMCTL_PROMETHEUS_VERSION", DefaultPrometheusVersion, "Prometheus version"},
		{"NOMCTL_INFINITY_PLUGIN_VERSION", DefaultInfinityPluginVersion, "Grafana Infinity datasource plugin version"},
		{"NOMCTL_GRAFANA_ADMIN_USER", DefaultGrafanaAdminUser, "Grafana admin user"},
		{"NOMCTL_GRAFANA_ADMIN_PASSWORD", DefaultGrafanaAdminPassword, "Grafana admin password; a non-default value is applied to Grafana on install"},
		{"NOMCTL_GRAFANA_HTTP_ADDR", DefaultGrafanaHTTPAddr, "Address Grafana listens on (0.0.0.0 to expose it)"},
	}
}
