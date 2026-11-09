package tui

import (
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/ui"
	"github.com/charmbracelet/huh"
)

const (
	menuManage           Action = "manage"
	menuBackups          Action = "backups"
	menuInstall          Action = "install"
	menuPillar           Action = "pillar-orchestrator"
	menuTools            Action = "tools"
	ActionBack           Action = "back"
	ActionUpgradeSource  Action = "upgrade-source"
	ActionUpgradeNomctl  Action = "upgrade-nomctl"
	ActionWalletBackup   Action = "backup-wallet"
	ActionWalletRestore  Action = "restore-wallet"
	ActionBackupSchedule Action = "backup-schedule"
	ActionCreatePoint    Action = "create-restore-point"
	ActionRestorePoint   Action = "restore-point"
	ActionManagePoints   Action = "manage-restore-points"
	ActionManageBackups  Action = "manage-backups"
	ActionPillarStatus   Action = "pillar-status"
	ActionPillarSetup    Action = "pillar-setup"
	ActionOrchStatus     Action = "orchestrator-status"
	ActionOrchLogs       Action = "orchestrator-logs"
	ActionOrchReset      Action = "orchestrator-reset"
)

type menuEntry struct {
	action             Action
	label, description string
}
type menuDefinition struct {
	title   string
	entries []menuEntry
}

var menus = map[Action]menuDefinition{
	"": {"nomctl", []menuEntry{
		{menuManage, "Manage node", "Dashboard, logs, service controls and configuration."},
		{menuBackups, "Backups & recovery", "Local chain backups, wallet backups and recovery."},
		{menuInstall, "Install & upgrade", "Deploy a node or Pillar, or upgrade an existing installation."},
		{menuPillar, "Pillar & orchestrator", "Producer keys and orchestrator service controls."},
		{menuTools, "Alerts & tools", "Telegram alerts, Grafana and support bundles."},
		{ActionExit, "Exit", "Close nomctl."},
	}},
	menuManage: {"Manage node", []menuEntry{
		{ActionStatus, "Live dashboard", "Watch sync progress, CPU, memory and node health."},
		{ActionMonitor, "View logs", "Follow node logs; Ctrl+C returns to this menu."},
		{ActionStart, "Start node", "Start the configured node service."},
		{ActionRestart, "Restart node", "Stop and start the configured node service."},
		{ActionStop, "Stop node", "Stop the node until you start it again."},
		{ActionConfig, "Node configuration", "Show or edit settings in config.json."},
	}},
	menuBackups: {"Backups & recovery", []menuEntry{
		{ActionBackup, "Backup chain data locally", "Copy chain data and create an archive in the local backup directory."},
		{ActionRestore, "Restore from backup", "Choose a local chain backup and verify it before restoring."},
		{ActionCreatePoint, "Create restore point", "Save chain data and the installed binary; protected from automatic deletion."},
		{ActionRestorePoint, "Restore from restore point", "Restore the matching chain data and node binary together."},
		{ActionManagePoints, "Manage restore points", "Inspect sizes, delete selected points or prune eligible points after confirmation."},
		{ActionManageBackups, "Manage backups", "Delete selected backups or prune old backups after reviewing the files and space to free."},
		{ActionWalletBackup, "Back up wallet & configuration", "Create an encrypted backup of wallet files and config.json."},
		{ActionWalletRestore, "Restore wallet & configuration", "Restore an encrypted wallet backup; keep current files aside."},
		{ActionBackupSchedule, "Schedule local backups", "Daily cron backups; keep 1–30 days (default 7), with a 75% disk limit."},
		{ActionBootstrap, `Restore from "trusted" bootstrap snapshot`, "Choose a publisher you trust; the snapshot hash checks integrity."},
		{ActionResync, "Resync from scratch", "Delete chain data and sync again; keep wallet and configuration."},
	}},
	menuInstall: {"Install & upgrade", []menuEntry{
		{ActionDeploy, "Deploy a node from source", "Choose a repository and branch; build and set up the node service."},
		{ActionPillarDep, "Deploy a Pillar", "Build official go-zenon master and configure the producer key."},
		{ActionUpgradeSource, "Upgrade a node from source", "Build first, then back up chain data and replace the installed binary."},
		{ActionUpgradeNode, "Upgrade a node from signed release", "Select a go-zenon-releases version; verify its signature before upgrading."},
		{ActionUpgradeNomctl, "Upgrade nomctl", "Download and install the latest nomctl release."},
	}},
	menuPillar: {"Pillar & orchestrator", []menuEntry{
		{ActionPillarStatus, "Pillar status", "Show the configured producer address and key file."},
		{ActionPillarSetup, "Set up producer key", "Create a producer key or configure an existing one."},
		{ActionOrchStatus, "Orchestrator status", "Show the state of the configured orchestrator service."},
		{ActionOrchLogs, "View orchestrator logs", "Follow orchestrator logs; Ctrl+C returns to this menu."},
		{ActionOrchReset, "Reset orchestrator", "Delete queues and events, then restart; confirmation required."},
	}},
	menuTools: {"Alerts & tools", []menuEntry{
		{ActionAlerts, "Telegram alerts", "Set up alerts, or show status when already paired."},
		{ActionAnalytics, "Set up Grafana dashboard", "Install and configure the analytics stack."},
		{ActionSupport, "Create support bundle", "Collect diagnostic information for troubleshooting."},
	}},
}

func menuEntries(menu Action) []menuEntry {
	entries := append([]menuEntry(nil), menus[menu].entries...)
	if menu != "" {
		entries = append(entries, menuEntry{ActionBack, "Back", "Return to the main menu."})
	}
	return entries
}

// MenuOptions returns the top-level groups with readable labels.
func MenuOptions() []huh.Option[string] { return optionsFor(menuEntries("")) }

func optionsFor(entries []menuEntry) []huh.Option[string] {
	options := make([]huh.Option[string], 0, len(entries))
	for _, e := range entries {
		options = append(options, huh.NewOption(e.label, string(e.action)))
	}
	return options
}

func chooseMenu(menu Action, previous Action) (Action, error) {
	entries := menuEntries(menu)
	chosen := string(previous)
	if chosen == "" {
		chosen = string(entries[0].action)
	}
	title := "nomctl"
	if menu != "" {
		title += " › " + menus[menu].title
	}
	sel := huh.NewSelect[string]().Title(ui.StyleHeader.Render(title)).Options(optionsFor(entries)...).Value(&chosen)
	sel.DescriptionFunc(func() string {
		for _, e := range entries {
			if string(e.action) == chosen {
				return e.description
			}
		}
		return ""
	}, &chosen)
	if err := runForm(sel); err != nil {
		return "", err
	}
	return Action(chosen), nil
}

// Run keeps users in the current submenu after an operation. Back/Esc moves up.
func Run(cfg *config.Config) error {
	printBanner()
	return runMenus(cfg, chooseMenu, Dispatch)
}

func runMenus(cfg *config.Config, choose func(Action, Action) (Action, error), dispatch func(*config.Config, Action) error) error {
	current := Action("")
	previous := map[Action]Action{}
	for {
		action, err := choose(current, previous[current])
		if errors.Is(err, ErrCancelled) {
			if current == "" {
				return nil
			}
			current = ""
			continue
		}
		if err != nil {
			return err
		}
		if action == ActionExit {
			return nil
		}
		if action == ActionBack {
			current = ""
			continue
		}
		previous[current] = action
		if _, ok := menus[action]; ok {
			current = action
			continue
		}
		if err := dispatch(cfg, action); err != nil && !errors.Is(err, ErrCancelled) {
			slog.Error(err.Error())
		}
		fmt.Fprintln(os.Stderr)
	}
}

// UpgradeNomctl is installed by cmd so the menu reuses the self-upgrader.
var UpgradeNomctl func() error

func upgradeNomctl() error {
	if UpgradeNomctl == nil {
		return errors.New("nomctl self-upgrade is unavailable")
	}
	ok, err := Confirm("Upgrade nomctl to its latest release?")
	if err != nil {
		return err
	}
	if !ok {
		return ErrCancelled
	}
	return UpgradeNomctl()
}
