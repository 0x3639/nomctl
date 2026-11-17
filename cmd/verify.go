package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/0x3639/nomctl/internal/config"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/0x3639/nomctl/internal/recovery"
	"github.com/spf13/cobra"
)

var verificationStateDir = recovery.StateDir

type verifyFlags struct {
	file, point, directory         string
	all, json, rehearse, scheduled bool
}

func (f verifyFlags) config() (config.Config, error) {
	selected := cfg
	if f.directory != "" {
		if !filepath.IsAbs(f.directory) {
			return selected, errors.New("backup directory must be absolute")
		}
		selected.BackupDir = f.directory
	}
	return selected, nil
}

func (f verifyFlags) targets() ([]recovery.Target, error) {
	selected, err := f.config()
	if err != nil {
		return nil, err
	}

	count := 0
	if f.file != "" {
		count++
	}
	if f.point != "" {
		count++
	}
	if f.all {
		count++
	}
	if count > 1 {
		return nil, errors.New("choose only one of --file, --point, or --all")
	}
	if f.file != "" {
		return []recovery.Target{recovery.Backup(selected, f.file)}, nil
	}
	if f.point != "" {
		t, err := recovery.Point(selected, f.point)
		if err != nil {
			return nil, err
		}
		return []recovery.Target{t}, nil
	}
	return recovery.List(selected)
}

func verificationAnnotations() map[string]string {
	a := diagnostic()
	a[annotationOptionalServiceTools] = "true"
	return a
}

func addVerifySelection(c *cobra.Command, f *verifyFlags) {
	c.Flags().StringVar(&f.directory, "directory", "", "absolute directory containing backups and restore points")
	c.Flags().StringVar(&f.file, "file", "", "verify one chain backup (path or name)")
	c.Flags().StringVar(&f.point, "point", "", "verify one full restore point by ID")
	c.Flags().BoolVar(&f.all, "all", false, "check all backups and restore points (default)")
	c.Flags().BoolVar(&f.json, "json", false, "print structured results")
}

func newVerifyCommand() *cobra.Command {
	var f verifyFlags
	c := &cobra.Command{Use: "verify", Short: "Verify backups and restore points without stopping znnd",
		Long: "Read recovery files and check checksums, archive contents, and saved znnd.\nBy default checks all artifacts for the configured service and backup directory.\nResults are saved; a failed artifact causes a nonzero exit. Nothing is pruned.\nFile integrity does not establish database compatibility or successful startup.",
		Args: cobra.NoArgs, Annotations: verificationAnnotations(),
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			var report recovery.Report
			var err error
			if f.scheduled {
				if f.file != "" || f.point != "" || f.all || f.rehearse || f.directory != "" {
					return errors.New("--scheduled cannot be combined with selection or rehearsal flags")
				}
				report, err = recovery.RunScheduled(ctx, verificationStateDir, operationLockPath)
			} else {
				targets, selectErr := f.targets()
				if selectErr != nil {
					return selectErr
				}
				selected, selectErr := f.config()
				if selectErr != nil {
					return selectErr
				}
				report, err = recovery.Run(ctx, selected, targets, recovery.Options{Rehearse: f.rehearse}, verificationStateDir, operationLockPath)
			}
			if f.json {
				return errors.Join(err, json.NewEncoder(cmd.OutOrStdout()).Encode(report))
			}
			recovery.WriteReport(cmd.OutOrStdout(), report)
			return err
		}}
	addVerifySelection(c, &f)
	c.Flags().BoolVar(&f.rehearse, "rehearse", false, "also extract privately after checking temporary space; never starts a node")
	c.Flags().BoolVar(&f.scheduled, "scheduled", false, "internal managed cron invocation")
	_ = c.Flags().MarkHidden("scheduled")
	c.AddCommand(newVerifyStatusCommand(), newVerifyScheduleCommand())
	return c
}

func newVerifyStatusCommand() *cobra.Command {
	var f verifyFlags
	c := &cobra.Command{Use: "status", Short: "Show saved verification results, marking changed files stale", Args: cobra.NoArgs, Annotations: verificationAnnotations(),
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			targets, err := f.targets()
			if err != nil {
				return err
			}
			if f.file == "" && f.point == "" {
				selected, err := f.config()
				if err != nil {
					return err
				}
				targets, err = recovery.ListStatus(selected, verificationStateDir)
				if err != nil {
					return err
				}
			}
			states := make([]recovery.Status, 0, len(targets))
			for _, t := range targets {
				states = append(states, recovery.SavedStatus(ctx, verificationStateDir, t))
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			s, err := recovery.ReadSchedule()
			if err != nil {
				return err
			}
			var a *recovery.ScheduledAttempt
			if s != nil {
				a, err = recovery.LastAttempt(verificationStateDir, *s)
				if err != nil {
					return err
				}
			}
			if f.json {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
					Files                []recovery.Status          `json:"files"`
					Schedule             *recovery.Schedule         `json:"schedule"`
					LastScheduledAttempt *recovery.ScheduledAttempt `json:"last_scheduled_attempt"`
					Limits               string                     `json:"limits"`
				}{states, s, a, recovery.Limits})
			}
			recovery.WriteStatus(cmd.OutOrStdout(), states)
			recovery.WriteSchedule(cmd.OutOrStdout(), s, a)
			return nil
		}}
	addVerifySelection(c, &f)
	return c
}

func newVerifyScheduleCommand() *cobra.Command {
	hour := 5
	var directory string
	c := &cobra.Command{Use: "schedule", Short: "Set up daily verification without running it now", Args: cobra.NoArgs, Annotations: diagnostic(),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withLock("schedule recovery verification", func() error {
				s, err := recovery.ReadSchedule()
				if err != nil {
					return err
				}
				selected := cfg
				if s != nil {
					selected = s.Config()
					if !cmd.Flags().Changed("hour") {
						hour = s.Hour
					}
				}
				if cmd.Flags().Changed("directory") {
					selected.BackupDir = directory
				}
				if err := recovery.SaveSchedule(selected, hour, s); err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), "Verification schedule saved; no verification runs now. Existing pause state is preserved.")
				return nil
			})
		}}
	c.Flags().StringVar(&directory, "directory", "", "absolute backup directory; preserves the saved directory when omitted")
	c.Flags().IntVar(&hour, "hour", 5, "daily hour, 0-23 in server time (minute 17)")
	for _, enabled := range []bool{false, true} {
		name := "pause"
		if enabled {
			name = "resume"
		}
		c.AddCommand(&cobra.Command{Use: name, Short: name + " daily verification", Args: cobra.NoArgs, Annotations: diagnostic(), RunE: func(cmd *cobra.Command, _ []string) error {
			return withLock(name+" recovery verification", func() error {
				s, err := recovery.ReadSchedule()
				if err != nil {
					return err
				}
				if err := recovery.SetScheduleEnabled(enabled, s); err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), "Verification schedule updated. Recovery files are unchanged.")
				return nil
			})
		}})
	}
	return c
}

func init() { rootCmd.AddCommand(newVerifyCommand()) }
