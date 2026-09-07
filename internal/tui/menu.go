package tui

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/huh"

	"github.com/0x3639/nomctl/internal/analytics"
	"github.com/0x3639/nomctl/internal/backup"
	"github.com/0x3639/nomctl/internal/bootstrap"
	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/deploy"
	"github.com/0x3639/nomctl/internal/lock"
	"github.com/0x3639/nomctl/internal/orchestrator"
	"github.com/0x3639/nomctl/internal/restore"
	"github.com/0x3639/nomctl/internal/resync"
	"github.com/0x3639/nomctl/internal/service"
	"github.com/0x3639/nomctl/internal/support"
	"github.com/0x3639/nomctl/internal/ui"
)

// Action identifies a menu entry.
type Action string

// Menu actions, in display order.
const (
	ActionDeploy    Action = "deploy"
	ActionRestart   Action = "restart"
	ActionStop      Action = "stop"
	ActionStart     Action = "start"
	ActionMonitor   Action = "monitor"
	ActionStatus    Action = "status"
	ActionAlerts    Action = "alerts"
	ActionSupport   Action = "support"
	ActionResync    Action = "resync"
	ActionBackup    Action = "backup"
	ActionRestore   Action = "restore"
	ActionBootstrap Action = "bootstrap"
	ActionOrch      Action = "orchestrator"
	ActionAnalytics Action = "analytics"
	ActionExit      Action = "exit"
)

// MenuOptions returns the labelled menu entries for the given config.
func MenuOptions(cfg config.Config) []huh.Option[string] {
	entries := []struct {
		action Action
		label  string
	}{
		{ActionDeploy, "Set up a Zenon Network node"},
		{ActionRestart, "Restart the " + cfg.ServiceName + " service"},
		{ActionStop, "Stop the " + cfg.ServiceName + " service"},
		{ActionStart, "Start the " + cfg.ServiceName + " service"},
		{ActionMonitor, "View " + cfg.BinaryName + " logs in real-time"},
		{ActionStatus, "Live node dashboard (sync, CPU, memory)"},
		{ActionAlerts, "Telegram alerts (set up or show status)"},
		{ActionSupport, "Create a support bundle for troubleshooting"},
		{ActionResync, "Resync the " + cfg.BinaryName + " node"},
		{ActionBackup, "Backup " + cfg.BinaryName + " data"},
		{ActionRestore, "Restore Zenon from a backup"},
		{ActionBootstrap, "Restore Zenon from a bootstrap snapshot"},
		{ActionAnalytics, "Set up a Grafana dashboard"},
		{ActionOrch, "Orchestrator (hard reset, status, logs)"},
		{ActionExit, ""},
	}
	opts := make([]huh.Option[string], 0, len(entries))
	for _, e := range entries {
		label := string(e.action)
		if e.label != "" {
			label = fmt.Sprintf("%s → %s", e.action, e.label)
		}
		opts = append(opts, huh.NewOption(label, string(e.action)))
	}
	return opts
}

// Run shows the main menu until the user exits.
func Run(cfg *config.Config) error {
	for {
		printBanner()
		choice, err := Select("CHOOSE AN ACTION:", MenuOptions(*cfg))
		if err != nil {
			if errors.Is(err, ErrCancelled) {
				return nil
			}
			return err
		}
		action := Action(choice)
		if action == ActionExit {
			return nil
		}
		if err := Dispatch(cfg, action); err != nil {
			if errors.Is(err, ErrCancelled) {
				slog.Warn("Cancelled")
			} else {
				slog.Error(err.Error())
			}
		}
		fmt.Fprintln(os.Stderr)
		again, err := Confirm("Return to main menu?")
		if err != nil || !again {
			return nil
		}
	}
}

func printBanner() {
	fmt.Fprintln(os.Stderr, ui.StyleBanner.Render(ui.Banner))
	fmt.Fprintln(os.Stderr, ui.StyleSubtitle.Render("Zenon Network · NoM node control"))
	fmt.Fprintln(os.Stderr)
}

// Dispatch runs one menu action interactively.
func Dispatch(cfg *config.Config, action Action) error {
	switch action {
	case ActionDeploy:
		return withLock("deploy", func() error { return Deploy(*cfg) })
	case ActionRestart:
		return service.Restart(cfg.ServiceName)
	case ActionStop:
		return service.Stop(cfg.ServiceName)
	case ActionStart:
		return service.Start(cfg.ServiceName)
	case ActionMonitor:
		return Monitor(*cfg, true, 20)
	case ActionStatus:
		return Top(*cfg, 2*time.Second, PillarName(), nil)
	case ActionAlerts:
		return AlertsAction()
	case ActionSupport:
		return SupportBundle(*cfg)
	case ActionResync:
		return Resync(*cfg)
	case ActionBackup:
		return withLock("backup", func() error { return Backup(cfg) })
	case ActionRestore:
		return Restore(*cfg)
	case ActionBootstrap:
		return Bootstrap(*cfg)
	case ActionAnalytics:
		return analytics.Install(*cfg)
	case ActionOrch:
		return OrchestratorMenu(*cfg)
	case ActionExit:
		return nil
	}
	return fmt.Errorf("unknown action %q", action)
}

// Version is the nomctl version string recorded in support bundles; the cmd
// package sets it at startup.
var Version = "dev"

// PillarName is installed by the cmd package; it returns the configured
// pillar name for the dashboard.
var PillarName = func() string { return "" }

// AlertsSetup and AlertsStatus are installed by the cmd package so the menu
// can reuse the command implementations without an import cycle.
var (
	AlertsSetup  func() error
	AlertsStatus func() error
	AlertsPaired func() bool
)

// AlertsAction runs setup when unpaired, otherwise shows status.
func AlertsAction() error {
	if AlertsSetup == nil || AlertsStatus == nil || AlertsPaired == nil {
		return errors.New("alerts are not available in this build")
	}
	if AlertsPaired() {
		return AlertsStatus()
	}
	return AlertsSetup()
}

// Orchestrator submenu actions.
const (
	orchStatus    = "status"
	orchLogs      = "logs"
	orchHardReset = "hard-reset"
	orchBack      = "back"
)

// OrchestratorMenu shows the orchestrator functions and runs the chosen
// one. It returns to the main menu on "Back".
func OrchestratorMenu(cfg config.Config) error {
	installed, err := orchestrator.Installed(cfg)
	if err != nil {
		return err
	}
	if !installed {
		slog.Warn(orchestrator.ErrNotInstalled.Error() + " (unit " + cfg.OrchestratorService + ")")
		return nil
	}
	choice, err := Select("Orchestrator", []huh.Option[string]{
		huh.NewOption("Hard reset (delete queues and events, restart)", orchHardReset),
		huh.NewOption("Show status", orchStatus),
		huh.NewOption("View orchestrator logs in real-time", orchLogs),
		huh.NewOption("Back", orchBack),
	})
	if err != nil {
		return err
	}
	switch choice {
	case orchHardReset:
		return OrchestratorHardReset(cfg)
	case orchStatus:
		st, err := service.Status(cfg.OrchestratorService)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "%s: %s\n", cfg.OrchestratorService, st)
		return nil
	case orchLogs:
		return MonitorUnit(cfg.OrchestratorService, true, 20)
	}
	return nil
}

// OrchestratorHardReset confirms, then runs the hard reset.
func OrchestratorHardReset(cfg config.Config) error {
	ok, err := Confirm(orchestrator.ConfirmText)
	if err != nil {
		return err
	}
	if !ok {
		slog.Warn("Hard reset cancelled by user")
		return nil
	}
	// The lock is taken only now so the prompt never holds it.
	return withLock("orchestrator hard-reset", func() error { return orchestrator.HardReset(cfg) })
}

// SupportBundle collects a bundle with defaults and prints where it went.
func SupportBundle(cfg config.Config) error {
	res, err := support.Collect(context.Background(), cfg, support.Options{Version: Version})
	if err != nil {
		return err
	}
	ui.Success("Support bundle created")
	fmt.Fprintf(os.Stderr, "\nDiagnostics directory: %s\nBundle:                %s\nCrash markers:         %s\n\nReview the bundle before sharing; configuration contents are never collected.\n", res.Dir, res.Archive, res.Markers)
	return nil
}

// withLock serialises node-data operations with the CLI commands and the
// scheduled backup timer.
func withLock(operation string, fn func() error) error {
	l, err := lock.Acquire(lock.DefaultPath, operation)
	if err != nil {
		return err
	}
	defer l.Release()
	return fn()
}

// Monitor ports monitor.sh: follow the journal, or show the last lines with
// a warning when the service is not running.
func Monitor(cfg config.Config, follow bool, lines int) error {
	return MonitorUnit(cfg.ServiceName, follow, lines)
}

// MonitorUnit is Monitor for any systemd unit.
func MonitorUnit(name string, follow bool, lines int) error {
	if follow && !service.IsActive(name) {
		slog.Warn(fmt.Sprintf("%s service is not running. Showing last %d log lines:", name, lines))
		follow = false
	}
	if follow {
		fmt.Fprintln(os.Stderr, ui.StyleBox.Render(fmt.Sprintf("Monitoring %s logs. Press Ctrl+C to stop.", name)))
	}
	return service.Logs(name, follow, lines)
}

// Deploy asks for repository and branch, then runs the deploy.
func Deploy(cfg config.Config) error {
	ui.Section(os.Stderr, "==== BUILD: Zenon Network from Source ====")

	opts := make([]huh.Option[string], 0, len(deploy.RepoChoices)+1)
	for _, c := range deploy.RepoChoices {
		opts = append(opts, huh.NewOption(fmt.Sprintf("%s → %s", c.Label, c.URL), c.URL))
	}
	opts = append(opts, huh.NewOption("custom → Provide a custom repository URL", "custom"))
	repoURL, err := Select("SELECT A REPOSITORY:", opts)
	if err != nil {
		return fmt.Errorf("repository selection cancelled: %w", err)
	}
	if repoURL == "custom" {
		custom, err := Input("Repository URL", "https://github.com/user/repo.git")
		if err != nil {
			return err
		}
		custom = strings.TrimSpace(custom)
		if custom == "" {
			repoURL = cfg.RepoURL
			slog.Info("No URL provided, using default: " + repoURL)
		} else {
			repoURL = custom
			slog.Info("Using custom repository: " + repoURL)
		}
	} else {
		ui.Success("You selected repository: " + repoURL)
	}

	slog.Info("Fetching branches from " + repoURL)
	var branches []string
	if err := ui.Step("Fetching branches...", func() error {
		var err error
		branches, err = deploy.ListBranches(repoURL)
		return err
	}); err != nil {
		return err
	}
	bopts := make([]huh.Option[string], 0, len(branches))
	for _, b := range branches {
		bopts = append(bopts, huh.NewOption(b, b))
	}
	branch, err := Select("SELECT A BRANCH:", bopts)
	if err != nil {
		return fmt.Errorf("branch selection cancelled: %w", err)
	}
	ui.Success("You selected branch: " + branch)

	return deploy.Run(cfg, repoURL, branch)
}

// Resync asks for confirmation before wiping chain data.
func Resync(cfg config.Config) error {
	ok, err := Confirm(resync.ConfirmText)
	if err != nil {
		return err
	}
	if !ok {
		slog.Warn("Resync cancelled by user")
		return nil
	}
	return withLock("resync", func() error { return resync.Run(cfg) })
}

// Bootstrap asks for the snapshot URL and whether to keep the previous
// data, confirms, then installs the snapshot.
func Bootstrap(cfg config.Config) error {
	url, err := Input("Snapshot URL (.zip; a .hash sidecar must sit next to it)", cfg.BootstrapURL)
	if err != nil {
		return err
	}
	url = strings.TrimSpace(url)
	if url == "" {
		url = cfg.BootstrapURL
	}
	if err := bootstrap.ValidateURL(url); err != nil {
		return err
	}
	keep, err := Confirm("Keep a copy of the current chain data under " + backup.RestoreDir(cfg) + "?\n(Answer No on a disk too small for both copies.)")
	if err != nil {
		return err
	}
	ok, err := Confirm(bootstrap.ConfirmText)
	if err != nil {
		return err
	}
	if !ok {
		slog.Warn("Bootstrap cancelled by user")
		return nil
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return withLock("bootstrap", func() error {
		return bootstrap.Run(ctx, cfg, bootstrap.Options{URL: url, Discard: !keep})
	})
}

// Restore lets the user pick an archive and restores it.
func Restore(cfg config.Config) error {
	archive, err := PickBackup(cfg)
	if err != nil {
		return err
	}
	return withLock("restore", func() error { return restore.Run(cfg, archive) })
}

var (
	reMaxBackups = regexp.MustCompile(`^[1-9][0-9]?$`)
	reDays       = regexp.MustCompile(`^[1-9][0-9]*$`)
	reHour       = regexp.MustCompile(`^([0-9]|1[0-9]|2[0-3])$`)
)

// Backup runs a backup and then offers to schedule recurring ones. The
// scheduling choices are written back to cfg so they persist for the rest of
// the menu session, as the exported variables did in the bash version.
func Backup(cfg *config.Config) error {
	if _, err := backup.Run(*cfg, backup.Options{Interactive: true}); err != nil {
		return err
	}
	ok, err := Confirm("Would you like to set up scheduled backups?")
	if err != nil || !ok {
		return err
	}

	maxIn, err := Input("Max backups to keep (1-30) ➜", strconv.Itoa(cfg.MaxBackups))
	if err != nil {
		return err
	}
	if n, valid := ParseMaxBackups(maxIn); valid {
		cfg.MaxBackups = n
	}

	cadence, err := Select("SELECT BACKUP CADENCE:", []huh.Option[string]{
		huh.NewOption("Daily → Backup every 24h", "daily"),
		huh.NewOption("Weekly → Backup every 7 days", "weekly"),
		huh.NewOption("Custom → Backup every N days", "custom"),
	})
	if err != nil {
		slog.Warn("No cadence selected, cancelling scheduled backup setup.")
		return nil
	}
	switch cadence {
	case "daily":
		cfg.BackupCadenceDays = 1
	case "weekly":
		cfg.BackupCadenceDays = 7
	case "custom":
		daysIn, err := Input("Number of days between backups (1-365) ➜", "3")
		if err != nil {
			return err
		}
		if n, valid := ParseCadenceDays(daysIn); valid {
			cfg.BackupCadenceDays = n
		} else {
			slog.Error("Invalid days input; defaulting to 1 day cadence.")
			cfg.BackupCadenceDays = 1
		}
	}

	hourIn, err := Input("Hour of day (0-23) to run backup ➜", "2")
	if err != nil {
		return err
	}
	if h, valid := ParseHour(hourIn); valid {
		cfg.BackupHour = h
	} else {
		cfg.BackupHour = -1
		slog.Error("Invalid hour input. Backup will run at a deterministic time between 2–4 AM.")
	}
	return backup.Schedule(*cfg)
}

// ParseMaxBackups validates a 1-30 input the way the bash prompt did.
func ParseMaxBackups(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if !reMaxBackups.MatchString(s) {
		return 0, false
	}
	n, _ := strconv.Atoi(s)
	return n, n <= 30
}

// ParseCadenceDays validates a 1-365 input.
func ParseCadenceDays(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if !reDays.MatchString(s) {
		return 0, false
	}
	n, _ := strconv.Atoi(s)
	return n, n <= 365
}

// ParseHour validates a 0-23 input.
func ParseHour(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if !reHour.MatchString(s) {
		return 0, false
	}
	n, _ := strconv.Atoi(s)
	return n, true
}
