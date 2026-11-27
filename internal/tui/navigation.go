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
	menuOrchestrator     Action = "orchestrator"
	menuTools            Action = "tools"
	ActionBack           Action = "back"
	ActionUpgradeSource  Action = "upgrade-source"
	ActionUpgradeNomctl  Action = "upgrade-nomctl"
	ActionWalletBackup   Action = "backup-wallet"
	ActionWalletRestore  Action = "restore-wallet"
	ActionBackupSchedule Action = "backup-schedule"
	ActionVerifyRecovery Action = "verify-recovery"
	ActionCreatePoint    Action = "create-restore-point"
	ActionRestorePoint   Action = "restore-point"
	ActionManagePoints   Action = "manage-restore-points"
	ActionManageBackups  Action = "manage-backups"
	ActionPillarStatus   Action = "pillar-status"
	ActionPillarSetup    Action = "pillar-setup"
	ActionMigration      Action = "migrate"
	ActionOrchInstall    Action = "orchestrator-install"
	ActionOrchUpgrade    Action = "orchestrator-upgrade"
	ActionOrchStatus     Action = "orchestrator-status"
	ActionOrchLogs       Action = "orchestrator-logs"
	ActionOrchBackup     Action = "orchestrator-backup"
	ActionOrchCopySync   Action = "orchestrator-copy-sync"
	ActionOrchConfig     Action = "orchestrator-config"
	ActionOrchTemplates  Action = "orchestrator-templates"
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
		{ActionDoctor, "Check my node", "Diagnose problems; show znnd version, commit and executable hashes."},
		{ActionMonitor, "View logs", "Follow node logs; Ctrl+C returns to this menu."},
		{ActionStart, "Start node", "Start the configured node service."},
		{ActionRestart, "Restart node", "Stop and start the configured node service."},
		{ActionStop, "Stop node", "Stop the node until you start it again."},
		{ActionConfig, "Node configuration", "Show or edit settings in config.json."},
	}},
	menuBackups: {"Backups & recovery", []menuEntry{
		{ActionRestore, "Restore from backup", "Restore chain data only; keep the installed znnd unchanged."},
		{ActionCreatePoint, "Create restore point", "Back up chain data and znnd, recording its version and hashes; protected from automatic deletion."},
		{ActionRestorePoint, "Restore from restore point", "Restore chain data and choose whether to revert to the saved znnd."},
		{ActionManagePoints, "Manage restore points", "Inspect sizes, delete selected points or prune eligible points after confirmation."},
		{ActionManageBackups, "Manage backups", "Delete selected backups or prune old backups after reviewing the files and space to free."},
		{ActionWalletBackup, "Back up wallet & configuration", "Create an encrypted backup of wallet files and config.json."},
		{ActionWalletRestore, "Restore wallet & configuration", "Restore an encrypted wallet backup; keep current files aside."},
		{ActionBackupSchedule, "Backup status & schedule", "View results; edit, pause or resume daily backups."},
		{ActionVerifyRecovery, "Verify recovery files", "Check backups and saved znnd; view results or schedule daily verification."},
		{ActionBootstrap, `Restore from "trusted" bootstrap snapshot`, "Choose a publisher you trust; the snapshot hash checks integrity."},
		{ActionResync, "Resync from scratch", "Delete chain data and sync again; keep wallet and configuration."},
	}},
	menuInstall: {"Install & upgrade", []menuEntry{
		{ActionMigration, "Migrate to a new device", "Preview, copy with rsync, and confirm a guarded handoff to a new server."},
		{ActionDeploy, "Deploy a node from source", "Choose a repository and branch; build and set up the node service."},
		{ActionPillarDep, "Deploy a Pillar", "Build official go-zenon master and configure the producer key."},
		{ActionUpgradeSource, "Upgrade a node from source", "Build first, then back up chain data and replace the installed binary."},
		{ActionUpgradeNode, "Upgrade a node from signed release", "Select a go-zenon-releases version; verify its signature before upgrading."},
		{ActionUpgradeNomctl, "Upgrade nomctl", "Install the latest release and reopen the menu with the new version."},
	}},
	menuPillar: {"Pillar & orchestrator", []menuEntry{
		{ActionPillarStatus, "Pillar status", "Show the configured producer address and key file."},
		{ActionPillarSetup, "Set up producer key", "Create a producer key or configure an existing one."},
		{menuOrchestrator, "Manage orchestrator", "Install, upgrade, configure, back up and operate the orchestrator."},
	}},
	menuOrchestrator: {"Orchestrator", []menuEntry{
		{ActionOrchInstall, "Install orchestrator", "Download a GitHub release, set up the service if needed, install and start."},
		{ActionOrchUpgrade, "Upgrade orchestrator", "Download a GitHub release, stop, replace /usr/local/bin/orchestrator and restart."},
		{ActionOrchStatus, "Orchestrator status", "Show the state of the configured orchestrator service."},
		{ActionOrchLogs, "View orchestrator logs", "Follow orchestrator logs; Ctrl+C returns to this menu."},
		{ActionOrchBackup, "Backup sync data", "Stop orchestrator, archive events/ and queues/ with a SHA-256 hash, then restart."},
		{ActionOrchCopySync, "Copy sync data to another server", "Copy events/ and queues/ over SSH with rsync and a remote safety backup."},
		{ActionOrchConfig, "Edit config.json", "Edit orchestrator config.json in $EDITOR with JSON validation and backup."},
		{ActionOrchTemplates, "Apply sync templates", "Apply Alchemy or dRPC (drpc.org) sync settings per EVM network (BNB Chain / Ethereum)."},
		{ActionOrchReset, "Reset orchestrator", "Delete queues and events, then restart; confirmation required."},
	}},
	menuTools: {"Alerts & tools", []menuEntry{
		{ActionAlerts, "Telegram alerts", "Set up alerts, or show status when already paired."},
		{ActionAnalytics, "Set up Grafana dashboard", "Install and configure the analytics stack."},
		{ActionSupport, "Create support bundle", "Collect diagnostic information for troubleshooting."},
	}},
}

var parentMenu = map[Action]Action{
	menuOrchestrator: menuPillar,
}

func menuEntries(menu Action) []menuEntry {
	entries := append([]menuEntry(nil), menus[menu].entries...)
	if menu != "" {
		backDesc := "Return to the main menu."
		if parent := parentMenu[menu]; parent != "" {
			backDesc = "Return to " + menus[parent].title + "."
		}
		entries = append(entries, menuEntry{ActionBack, "Back", backDesc})
	}
	return entries
}

func menuTitle(menu Action) string {
	if menu == "" {
		return "nomctl"
	}
	var chain []string
	for cur := menu; cur != ""; cur = parentMenu[cur] {
		chain = append([]string{menus[cur].title}, chain...)
	}
	title := "nomctl"
	for _, part := range chain {
		title += " › " + part
	}
	return title
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
	title := menuTitle(menu)
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
			current = parentMenu[current]
			continue
		}
		if err != nil {
			return err
		}
		if action == ActionExit {
			return nil
		}
		if action == ActionBack {
			current = parentMenu[current]
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
	ok, err := Confirm("Upgrade nomctl to its latest release? The menu will reopen with the new version after installation.")
	if err != nil {
		return err
	}
	if !ok {
		return ErrCancelled
	}
	return UpgradeNomctl()
}
