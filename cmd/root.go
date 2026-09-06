// Package cmd defines the cobra command tree. Every TUI action is also
// reachable here as a subcommand for non-interactive automation.
package cmd

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/execx"
	"github.com/0x3639/nomctl/internal/lock"
	"github.com/0x3639/nomctl/internal/logx"
	"github.com/0x3639/nomctl/internal/preflight"
	"github.com/0x3639/nomctl/internal/service"
	"github.com/0x3639/nomctl/internal/ui"
)

// Build information, injected via -ldflags "-X ...".
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// annotation key marking commands that must run as root.
const annotationRoot = "nomctl.requiresRoot"

var (
	cfg           config.Config
	closeLog      = func() {}
	flagDebug     bool
	flagLogFile   string
	flagNoPreflig bool
)

var rootCmd = &cobra.Command{
	Use:   "nomctl",
	Short: "Deploy and operate Zenon Network (NoM) nodes",
	Long: strings.TrimSpace(`
nomctl deploys, backs up, restores and operates a Zenon Network node.

Run without arguments to open the interactive menu. Every menu action is also
available as a subcommand for automation. Most commands must run as root.

Configuration comes from NOMCTL_* environment variables; flags override them.
Run "nomctl env" to list every variable with its default.`),
	SilenceUsage:  true,
	SilenceErrors: true,
	Version:       versionString(),
	Annotations:   map[string]string{annotationRoot: "true"},
	PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
		return setup(cmd)
	},
	PersistentPostRun: func(*cobra.Command, []string) { closeLog() },
	RunE: func(*cobra.Command, []string) error {
		return runTUI()
	},
}

func init() {
	rootCmd.PersistentFlags().BoolVar(&flagDebug, "debug", false, "verbose logging and visible command output (NOMCTL_DEBUG)")
	rootCmd.PersistentFlags().StringVar(&flagLogFile, "log-file", "", "log file path (NOMCTL_LOG_FILE)")
	rootCmd.PersistentFlags().BoolVar(&flagNoPreflig, "skip-preflight", false, "skip CPU/RAM/NTP/Internet pre-flight checks (NOMCTL_SKIP_PREFLIGHT)")
	rootCmd.SetVersionTemplate("nomctl {{.Version}}\n")
}

func versionString() string {
	return fmt.Sprintf("%s (commit %s, built %s, %s/%s)", version, commit, date, runtime.GOOS, runtime.GOARCH)
}

// Execute runs the CLI and returns the process exit code.
func Execute() int {
	if err := rootCmd.Execute(); err != nil {
		if errors.Is(err, errCancelled) {
			return 130
		}
		slog.Error(err.Error())
		closeLog()
		return 1
	}
	return 0
}

var errCancelled = errors.New("cancelled")

// applyFlags maps a command to the function that copies its flags into cfg
// and validates the result; it runs inside setup before pre-flight checks.
var applyFlags = map[*cobra.Command]func(*cobra.Command) error{}

// setup loads configuration, applies global flags, configures logging and
// enforces the root requirement plus pre-flight checks.
func setup(cmd *cobra.Command) error {
	requiresRoot := cmd.Annotations[annotationRoot] == "true"
	isRoot := os.Geteuid() == 0
	if requiresRoot && !isRoot {
		logx.Setup(false, "")
		return fmt.Errorf("%s must be run as root (try: sudo %s)", cmd.CommandPath(), cmd.CommandPath())
	}

	c, err := config.Load()
	if err != nil {
		logx.Setup(false, "")
		return err
	}
	if cmd.Flags().Changed("debug") {
		c.Debug = flagDebug
	}
	if cmd.Flags().Changed("log-file") {
		c.LogFile = flagLogFile
	}
	if cmd.Flags().Changed("skip-preflight") {
		c.SkipPreflight = flagNoPreflig
	}
	cfg = c

	// Only privileged commands write the log file (it lives under /var/log).
	var logFile string
	if requiresRoot && isRoot {
		logFile = cfg.LogFile
	}
	f, closer := logx.Setup(cfg.Debug, logFile)
	closeLog = closer
	if f != nil {
		execx.Configure(cfg.Debug, f)
	} else {
		execx.Configure(cfg.Debug, nil)
	}
	ui.SetDebug(cfg.Debug)
	slog.Debug("configuration loaded", "config", cfg.Redacted())

	// Command-specific flag handling and validation happens here so that bad
	// arguments are rejected before the (slow) pre-flight checks run.
	if apply := applyFlags[cmd]; apply != nil {
		if err := apply(cmd); err != nil {
			return err
		}
	}

	if requiresRoot {
		if err := service.Available(); err != nil {
			return err
		}
	}
	if requiresRoot && !cfg.SkipPreflight {
		ui.Section(os.Stderr, "==== PRE-FLIGHT CHECKS ====")
		if err := preflight.Run(); err != nil {
			return fmt.Errorf("pre-flight check failed: %w", err)
		}
		logx.Success("Pre-flight checks complete. Systems nominal. Go for launch.")
	}
	return nil
}

// withLock runs fn while holding the node-data lock, so backup, restore,
// resync and deploy never overlap each other or the scheduled backup timer.
func withLock(operation string, fn func() error) error {
	l, err := lock.Acquire(lock.DefaultPath, operation)
	if err != nil {
		return err
	}
	defer l.Release()
	return fn()
}

// rootOnly returns the annotation map marking a command as privileged.
func rootOnly() map[string]string { return map[string]string{annotationRoot: "true"} }
