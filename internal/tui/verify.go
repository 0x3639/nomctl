package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/recovery"
	"github.com/charmbracelet/huh"
)

func VerifyRecoveryFiles(cfg config.Config) error {
	for {
		fmt.Fprintf(os.Stderr, "Recovery files: %s (service: %s)\n", cfg.BackupDir, cfg.ServiceName)
		targets, err := recovery.List(cfg)
		if err != nil {
			return err
		}
		s, err := recovery.ReadSchedule()
		if err != nil {
			return err
		}
		var a *recovery.ScheduledAttempt
		if s != nil {
			a, err = recovery.LastAttempt(recovery.StateDir, *s)
			if err != nil {
				return err
			}
		}
		recovery.WriteSchedule(os.Stderr, s, a)
		opts := []huh.Option[string]{huh.NewOption("Verify all recovery files", "all"), huh.NewOption("Choose one backup or restore point", "one"), huh.NewOption("View saved results (recheck file hashes)", "status"), huh.NewOption("Set up or edit daily verification", "schedule")}
		if s != nil {
			if s.Enabled {
				opts = append(opts, huh.NewOption("Pause daily verification", "pause"))
			} else {
				opts = append(opts, huh.NewOption("Resume daily verification", "resume"))
			}
		}
		opts = append(opts, huh.NewOption("Back", "back"))
		choice, err := Select("VERIFY RECOVERY FILES:", opts)
		if err != nil {
			return err
		}
		switch choice {
		case "back":
			return nil
		case "status":
			fmt.Fprintln(os.Stderr, "Rechecking saved file hashes; large archives can take time. Ctrl+C cancels…")
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			states := make([]recovery.Status, 0, len(targets))
			for _, target := range targets {
				states = append(states, recovery.SavedStatus(ctx, recovery.StateDir, target))
				if ctx.Err() != nil {
					break
				}
			}
			statusErr := ctx.Err()
			stop()
			if statusErr != nil {
				return ErrCancelled
			}
			recovery.WriteStatus(os.Stderr, states)
		case "schedule":
			selected := cfg
			hour := 5
			if s != nil {
				selected = s.Config()
				hour = s.Hour
			}
			directory, err := Input("Directory containing backups and restore points", selected.BackupDir)
			if err != nil {
				return err
			}
			if strings.TrimSpace(directory) == "" {
				directory = selected.BackupDir
			}
			if !filepath.IsAbs(directory) {
				return fmt.Errorf("choose an absolute backup directory")
			}
			selected.BackupDir = directory
			text, err := Input("Daily verification hour (0–23, server time; runs at minute 17)", strconv.Itoa(hour))
			if err != nil {
				return err
			}
			if strings.TrimSpace(text) == "" {
				text = strconv.Itoa(hour)
			}
			hour, ok := ParseHour(text)
			if !ok {
				return fmt.Errorf("choose an hour from 0 to 23")
			}
			ok, err = Confirm(fmt.Sprintf("Schedule daily verification of all recovery files in %s for %s?\nChecks read the archives while znnd keeps running. Busy runs are skipped.\nAn existing paused schedule stays paused. No verification runs now.", selected.BackupDir, selected.ServiceName))
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			if err := withLock("schedule recovery verification", func() error { return recovery.SaveSchedule(selected, hour, s) }); err != nil {
				return err
			}
		case "pause", "resume":
			if err := withLock(choice+" recovery verification", func() error { return recovery.SetScheduleEnabled(choice == "resume", s) }); err != nil {
				return err
			}
		case "all", "one":
			if len(targets) == 0 {
				return fmt.Errorf("no recovery files found in %s", cfg.BackupDir)
			}
			if choice == "one" {
				options := make([]huh.Option[string], 0, len(targets))
				for i, t := range targets {
					options = append(options, huh.NewOption(fmt.Sprintf("%s · %s", t.Kind, filepath.Base(t.Path)), strconv.Itoa(i)))
				}
				picked, err := Select("CHOOSE RECOVERY FILES:", options)
				if err != nil {
					return err
				}
				i, err := strconv.Atoi(picked)
				if err != nil || i < 0 || i >= len(targets) {
					return fmt.Errorf("invalid recovery selection")
				}
				targets = []recovery.Target{targets[i]}
			}
			mode, err := Select("VERIFICATION MODE:", []huh.Option[string]{huh.NewOption("Check integrity (no extraction)", "integrity"), huh.NewOption("Check integrity and rehearse extraction (needs temporary disk space)", "rehearse")})
			if err != nil {
				return err
			}
			fmt.Fprintln(os.Stderr, "Verifying recovery files; znnd keeps running. Ctrl+C cancels.\n"+recovery.Limits)
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			report, err := recovery.Run(ctx, cfg, targets, recovery.Options{Rehearse: mode == "rehearse"}, recovery.StateDir, operationLockPath)
			stop()
			recovery.WriteReport(os.Stderr, report)
			if errors.Is(err, context.Canceled) {
				return ErrCancelled
			}
			if err != nil {
				return err
			}
		}
	}
}
