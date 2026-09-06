package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/0x3639/nomctl/internal/alertproto"
	"github.com/0x3639/nomctl/internal/alerts"
	"github.com/0x3639/nomctl/internal/backup"
	"github.com/0x3639/nomctl/internal/logx"
	"github.com/0x3639/nomctl/internal/metrics"
	"github.com/0x3639/nomctl/internal/node"
	"github.com/0x3639/nomctl/internal/service"
	"github.com/0x3639/nomctl/internal/tui"
	"github.com/0x3639/nomctl/internal/ui"
	"github.com/0x3639/nomctl/internal/update"
)

var (
	flagAlertsCode  string
	flagAlertsName  string
	flagAlertsRelay string
)

var alertsCmd = &cobra.Command{
	Use:   "alerts",
	Short: "Telegram alerts for this node",
	Long: `Pairs this node with the nomctl Telegram bot through a relay and runs a
background service that reports state changes: service down, crash loop,
sync stalled or behind, not enough peers, disk/memory/open files high,
backup overdue, RPC unreachable. The relay also reports a node that stops
sending heartbeats. Send /start to the bot to get a pairing code.`,
}

var alertsSetupCmd = &cobra.Command{
	Use:         "setup",
	Short:       "Pair with the Telegram bot and start the alerts service",
	Args:        cobra.NoArgs,
	Annotations: diagnostic(),
	RunE: func(cmd *cobra.Command, _ []string) error {
		return alertsSetup(cmd)
	},
}

func alertsSetup(cmd *cobra.Command) error {
	if !ui.Interactive() && (flagAlertsCode == "" || flagAlertsName == "") {
		return errors.New("no terminal: pass --code and --name")
	}
	relayURL := flagAlertsRelay
	if relayURL == "" {
		relayURL = os.Getenv("NOMCTL_RELAY_URL")
	}
	if relayURL == "" {
		if existing, err := alerts.Load(alerts.DefaultConfigPath); err == nil && existing.RelayURL != "" {
			relayURL = existing.RelayURL
		} else {
			relayURL = alerts.DefaultRelayURL
		}
	}
	code := strings.TrimSpace(flagAlertsCode)
	if code == "" {
		fmt.Fprintln(os.Stderr, ui.StyleHeader.Render("Open the nomctl alerts bot in Telegram, send /start, and enter the code it replies with."))
		var err error
		code, err = tui.Input("Pairing code", "AB3K7QWX")
		if err != nil {
			return err
		}
		code = strings.TrimSpace(code)
	}
	if code == "" {
		return errors.New("a pairing code is required")
	}
	host, _ := os.Hostname()
	name := strings.TrimSpace(flagAlertsName)
	if name == "" {
		var err error
		name, err = tui.Input("Node name (shown in every alert)", host)
		if err != nil {
			return err
		}
		name = strings.TrimSpace(name)
		if name == "" {
			name = host
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := alerts.Pair(ctx, relayURL, alertproto.PairRequest{Code: code, Name: name, Host: host, Version: versionString()})
	if err != nil {
		return err
	}
	acfg := alerts.DefaultConfig()
	if existing, err := alerts.Load(alerts.DefaultConfigPath); err == nil {
		acfg.Rules = existing.Rules // keep thresholds across re-pairing
		acfg.Interval = existing.Interval
	}
	acfg.RelayURL, acfg.NodeID, acfg.Secret, acfg.Name = relayURL, resp.NodeID, resp.Secret, name
	// The node name doubles as the pillar name when a pillar has it.
	acfg.PillarName = ""
	if info, err := lookupPillar(ctx, name); err != nil {
		fmt.Fprintf(os.Stderr, "could not check whether %q is a pillar (%v); set it later with: nomctl alerts set pillar.name %s\n", name, err, name)
	} else if info != nil {
		acfg.PillarName = info.Name
		logx.Success(fmt.Sprintf("Monitoring pillar %s (rank %d): missed momentums will alert", info.Name, info.Rank))
	} else {
		fmt.Fprintf(os.Stderr, "No pillar named %q; monitoring as a full node. If this node runs a pillar: nomctl alerts set pillar.name <pillar>\n", name)
	}
	if err := acfg.Save(alerts.DefaultConfigPath); err != nil {
		return err
	}
	logx.Success(fmt.Sprintf("Paired as %q with relay %s", name, relayURL))

	client, err := alerts.NewClient(acfg, versionString())
	if err != nil {
		return err
	}
	if err := alerts.InstallUnit(cfg); err != nil {
		// The relay already knows this node; without a daemon it would raise
		// node_silent in five minutes, so undo the pairing.
		_ = alerts.UninstallUnit()
		unpairCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if uerr := client.Unpair(unpairCtx); uerr != nil && !errors.Is(uerr, alerts.ErrUnpaired) {
			return fmt.Errorf("install %s failed (%w) and the relay could not be told (%w); the credentials are kept in %s, run: sudo nomctl alerts unpair", alerts.UnitName, err, uerr, alerts.DefaultConfigPath)
		}
		_ = os.Remove(alerts.DefaultConfigPath)
		return fmt.Errorf("install %s: %w (pairing rolled back)", alerts.UnitName, err)
	}
	logx.Success(alerts.UnitName + ".service enabled and started")

	if err := client.Alert(ctx, alertproto.AlertRequest{Alert: "test", State: alertproto.Info, Severity: alertproto.InfoSev,
		Title: "test alert", Detail: "sent by nomctl alerts setup", At: time.Now()}); err != nil {
		return fmt.Errorf("paired, but the test alert failed: %w", err)
	}
	logx.Success("Test alert delivered; check Telegram")
	fmt.Fprintln(cmd.OutOrStdout(), "\nManage alerts with: nomctl alerts status | list | enable | disable | set | test | unpair")
	return nil
}

var alertsRunCmd = &cobra.Command{
	Use:         "run",
	Short:       "Run the alerts daemon in the foreground (used by the systemd unit)",
	Args:        cobra.NoArgs,
	Annotations: diagnostic(),
	RunE: func(*cobra.Command, []string) error {
		acfg, err := alerts.Load(alerts.DefaultConfigPath)
		if err != nil {
			return fmt.Errorf("alerts are not set up (%w); run: sudo nomctl alerts setup", err)
		}
		if !acfg.Paired() {
			return errors.New("alerts are not paired; run: sudo nomctl alerts setup")
		}
		client, err := alerts.NewClient(acfg, versionString())
		if err != nil {
			return err
		}
		alerts.BackupChecker = backupChecker
		alerts.NewerVersion = update.Newer
		if cfg.UpdateCheck {
			alerts.UpdateChecker = updateChecker
		}
		d := alerts.NewDaemon(acfg, alerts.DefaultConfigPath, alerts.DefaultStatePath, metrics.NewSampler(cfg), client)

		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		reload := make(chan struct{}, 1)
		hup := make(chan os.Signal, 1)
		signal.Notify(hup, syscall.SIGHUP)
		go func() {
			for range hup {
				select {
				case reload <- struct{}{}:
				default:
				}
			}
		}()
		return d.Run(ctx, reload)
	},
}

// lookupPillar asks the local node whether a pillar with this name exists.
func lookupPillar(ctx context.Context, name string) (*node.PillarInfo, error) {
	return node.New(node.DefaultURL).PillarByName(ctx, name)
}

// updateChecker feeds the update_available rule from the cached check.
func updateChecker() alerts.UpdateInfo {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c := update.Run(ctx, update.Options{Repo: cfg.ReleaseRepo, NodeRepo: cfg.RepoURL, NodeBranch: cfg.BranchName})
	info := alerts.UpdateInfo{NomctlLatest: c.NomctlLatest, NomctlRunning: version, NodeBranch: c.NodeBranch}
	if c.NodeRemote != "" {
		if commit, err := node.New(node.DefaultURL).ProcessInfo(ctx); err == nil && commit.Commit != "" {
			info.NodeBehind = !update.SameCommit(commit.Commit, c.NodeRemote)
		}
	}
	return info
}

// backupChecker feeds the backup_stale rule from the timer state and archives.
// The cadence comes from the installed timer unit, not this process's env.
func backupChecker() alerts.BackupInfo {
	info := alerts.BackupInfo{TimerEnabled: service.IsEnabled(backup.TimerName + ".timer"), CadenceDays: cfg.BackupCadenceDays}
	if !info.TimerEnabled {
		return info
	}
	if days, ok := backup.InstalledCadence(); ok {
		info.CadenceDays = days
	}
	if archives, err := backup.List(cfg); err == nil && len(archives) > 0 {
		info.Newest = archives[0].ModTime
	}
	return info
}

var alertsStatusCmd = &cobra.Command{
	Use:         "status",
	Short:       "Show pairing, service and per-alert state",
	Args:        cobra.NoArgs,
	Annotations: diagnostic(),
	RunE: func(cmd *cobra.Command, _ []string) error {
		out := cmd.OutOrStdout()
		acfg, err := alerts.Load(alerts.DefaultConfigPath)
		if err != nil || !acfg.Paired() {
			fmt.Fprintln(out, "Alerts are not set up. Run: sudo nomctl alerts setup")
			return nil
		}
		fmt.Fprintf(out, "%-10s %s (node %s)\n", "Node", acfg.Name, acfg.NodeID)
		if acfg.PillarName != "" {
			fmt.Fprintf(out, "%-10s %s\n", "Pillar", acfg.PillarName)
		} else {
			fmt.Fprintf(out, "%-10s none (full node); set with: nomctl alerts set pillar.name <pillar>\n", "Pillar")
		}
		fmt.Fprintf(out, "%-10s %s\n", "Relay", acfg.RelayURL)
		svc := "inactive"
		if service.IsActive(alerts.UnitName) {
			svc = "active"
		}
		fmt.Fprintf(out, "%-10s %s.service %s\n", "Service", alerts.UnitName, svc)
		st, err := alerts.LoadState(alerts.DefaultStatePath)
		if err != nil {
			fmt.Fprintf(out, "%-10s no state file yet (%v)\n", "Daemon", err)
			return nil
		}
		hb := "never"
		if !st.LastHeartbeatOK.IsZero() {
			hb = metrics.HumanDuration(time.Since(st.LastHeartbeatOK)) + " ago"
		}
		fmt.Fprintf(out, "%-10s started %s, last heartbeat ok %s\n", "Daemon", metrics.HumanDuration(time.Since(st.Started))+" ago", hb)
		if st.Unpaired {
			fmt.Fprintf(out, "%-10s UNPAIRED by the operator; run: sudo nomctl alerts setup\n", "Warning")
		} else if st.LastError != "" {
			fmt.Fprintf(out, "%-10s %s\n", "Last error", st.LastError)
		}
		fmt.Fprintln(out)
		for _, name := range alerts.RuleNames() {
			rc := acfg.Rules[name]
			if !rc.Enabled {
				fmt.Fprintf(out, "  %-18s disabled\n", name)
				continue
			}
			as, ok := st.Alerts[name]
			switch {
			case !ok:
				fmt.Fprintf(out, "  %-18s ok\n", name)
			case as.Firing:
				fmt.Fprintf(out, "  %-18s FIRING since %s: %s\n", name, metrics.HumanDuration(time.Since(as.Since))+" ago", as.Detail)
			default:
				fmt.Fprintf(out, "  %-18s ok\n", name)
			}
		}
		return nil
	},
}

var alertsListCmd = &cobra.Command{
	Use:         "list",
	Short:       "List alerts with their settings",
	Args:        cobra.NoArgs,
	Annotations: diagnostic(),
	RunE: func(cmd *cobra.Command, _ []string) error {
		acfg, err := alerts.Load(alerts.DefaultConfigPath)
		if err != nil {
			acfg = alerts.DefaultConfig()
		}
		out := cmd.OutOrStdout()
		for _, name := range alerts.RuleNames() {
			rc := acfg.Rules[name]
			info, _ := alertproto.Lookup(name)
			state := "enabled"
			if !rc.Enabled {
				state = "disabled"
			}
			var th []string
			for k, v := range rc.Thresholds {
				th = append(th, fmt.Sprintf("%s=%g", k, v))
			}
			fmt.Fprintf(out, "%-18s %-8s %-9s %s\n", name, string(info.Severity), state, strings.Join(th, " "))
		}
		fmt.Fprintf(out, "%-18s %-8s %-9s %s\n", "node_silent", "critical", "relay", "raised by the relay after 5m without a heartbeat")
		if acfg.PillarName == "" {
			fmt.Fprintln(out, "\npillar_missed is inactive until a pillar name is set: nomctl alerts set pillar.name <pillar>")
		}
		return nil
	},
}

func alertsToggle(enable bool) func(*cobra.Command, []string) error {
	return func(_ *cobra.Command, args []string) error {
		acfg, err := alerts.Load(alerts.DefaultConfigPath)
		if err != nil {
			return fmt.Errorf("alerts are not set up (%w); run: sudo nomctl alerts setup", err)
		}
		if err := acfg.Set(args[0]+".enabled", fmt.Sprint(enable)); err != nil {
			return err
		}
		if err := acfg.Save(alerts.DefaultConfigPath); err != nil {
			return err
		}
		_ = alerts.ReloadDaemon()
		if enable {
			logx.Success(args[0] + " enabled")
		} else {
			logx.Success(args[0] + " disabled")
		}
		return nil
	}
}

var alertsEnableCmd = &cobra.Command{Use: "enable <alert>", Short: "Enable an alert", Args: cobra.ExactArgs(1), Annotations: rootOnly(), RunE: alertsToggle(true)}
var alertsDisableCmd = &cobra.Command{Use: "disable <alert>", Short: "Disable an alert", Args: cobra.ExactArgs(1), Annotations: rootOnly(), RunE: alertsToggle(false)}

var alertsSetCmd = &cobra.Command{
	Use:         "set <alert>.<setting> <value>",
	Short:       "Change an alert threshold (e.g. disk_low.min_free_gb 20) or pillar.name",
	Args:        cobra.ExactArgs(2),
	Annotations: rootOnly(),
	RunE: func(_ *cobra.Command, args []string) error {
		acfg, err := alerts.Load(alerts.DefaultConfigPath)
		if err != nil {
			return fmt.Errorf("alerts are not set up (%w); run: sudo nomctl alerts setup", err)
		}
		if args[0] == "pillar.name" && strings.TrimSpace(args[1]) != "" {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			info, err := lookupPillar(ctx, strings.TrimSpace(args[1]))
			if err != nil {
				return fmt.Errorf("cannot verify pillar %q with the local node: %w", args[1], err)
			}
			if info == nil {
				return fmt.Errorf("no pillar named %q is registered", args[1])
			}
			args[1] = info.Name
		}
		if err := acfg.Set(args[0], args[1]); err != nil {
			return err
		}
		if err := acfg.Save(alerts.DefaultConfigPath); err != nil {
			return err
		}
		_ = alerts.ReloadDaemon()
		logx.Success(args[0] + " = " + args[1])
		return nil
	},
}

var alertsTestCmd = &cobra.Command{
	Use:         "test",
	Short:       "Send a test message to Telegram",
	Args:        cobra.NoArgs,
	Annotations: diagnostic(),
	RunE: func(*cobra.Command, []string) error {
		acfg, err := alerts.Load(alerts.DefaultConfigPath)
		if err != nil || !acfg.Paired() {
			return errors.New("alerts are not set up; run: sudo nomctl alerts setup")
		}
		client, err := alerts.NewClient(acfg, versionString())
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := client.Alert(ctx, alertproto.AlertRequest{Alert: "test", State: alertproto.Info, Severity: alertproto.InfoSev, Title: "test alert", Detail: "sent by nomctl alerts test", At: time.Now()}); err != nil {
			return err
		}
		logx.Success("Test alert delivered; check Telegram")
		return nil
	},
}

var flagAlertsForce bool

var alertsUnpairCmd = &cobra.Command{
	Use:   "unpair",
	Short: "Stop the alerts service and forget the pairing",
	Long: `Tells the relay to forget this node, then stops the service and removes the
local credentials. If the relay cannot be reached the credentials are kept so
you can retry; --force removes them anyway (use /unpair in Telegram to clean
up the relay side).`,
	Args:        cobra.NoArgs,
	Annotations: rootOnly(),
	RunE: func(*cobra.Command, []string) error {
		acfg, err := alerts.Load(alerts.DefaultConfigPath)
		if err == nil && acfg.Paired() {
			client, err := alerts.NewClient(acfg, versionString())
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			err = client.Unpair(ctx)
			cancel()
			if err != nil && !errors.Is(err, alerts.ErrUnpaired) {
				if !flagAlertsForce {
					return fmt.Errorf("relay could not be told (%w); credentials kept, retry later or use --force and then /unpair %s in Telegram", err, acfg.Name)
				}
				fmt.Fprintln(os.Stderr, "relay could not be told; removing local state anyway (--force):", err)
			}
		}
		if err := alerts.UninstallUnit(); err != nil {
			return err
		}
		if err := os.Remove(alerts.DefaultConfigPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		_ = os.Remove(alerts.DefaultStatePath)
		logx.Success("Alerts unpaired and service removed")
		return nil
	},
}

func init() {
	tui.AlertsSetup = func() error { return alertsSetup(alertsSetupCmd) }
	tui.PillarName = pillarName
	tui.AlertsStatus = func() error { return alertsStatusCmd.RunE(alertsStatusCmd, nil) }
	tui.AlertsPaired = func() bool {
		acfg, err := alerts.Load(alerts.DefaultConfigPath)
		return err == nil && acfg.Paired()
	}
	alertsSetupCmd.Flags().StringVar(&flagAlertsCode, "code", "", "pairing code from the Telegram bot (prompted if omitted)")
	alertsSetupCmd.Flags().StringVar(&flagAlertsName, "name", "", "node name shown in alerts (prompted if omitted; default hostname)")
	alertsSetupCmd.Flags().StringVar(&flagAlertsRelay, "relay", "", "relay URL (NOMCTL_RELAY_URL; default built in)")
	alertsUnpairCmd.Flags().BoolVar(&flagAlertsForce, "force", false, "remove local credentials even if the relay cannot be reached")
	alertsCmd.AddCommand(alertsSetupCmd, alertsRunCmd, alertsStatusCmd, alertsListCmd, alertsEnableCmd, alertsDisableCmd, alertsSetCmd, alertsTestCmd, alertsUnpairCmd)
	rootCmd.AddCommand(alertsCmd)
}
