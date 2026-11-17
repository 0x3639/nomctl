package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/migrate"
	"github.com/charmbracelet/huh"
)

func migrationInput(title, fallback string) (string, error) {
	v, err := Input(title+" (blank: "+fallback+")", fallback)
	if v == "" {
		v = fallback
	}
	return v, err
}

func migrationPlan(ctx context.Context, cfg config.Config, co *migrate.Coordinator) error {
	conn := migrate.Connection{}
	var err error
	conn.Host, err = Input("Trusted SSH destination (for example root@new-server)", "")
	if err != nil {
		return err
	}
	port, err := migrationInput("SSH port", "22")
	if err != nil {
		return err
	}
	conn.Port, err = strconv.Atoi(port)
	if err != nil {
		return errors.New("SSH port must be a number")
	}
	conn.Nomctl, err = migrationInput("Installed nomctl on the replacement", "/usr/local/bin/nomctl")
	if err != nil {
		return err
	}
	conn.Sudo, err = Confirm("Use noninteractive sudo on the replacement?")
	if err != nil {
		return err
	}
	dest := migrate.InstallationFor(cfg)
	dest.Data, err = migrationInput("New data directory (must not exist)", dest.Data)
	if err != nil {
		return err
	}
	dest.Binary, err = migrationInput("New znnd path (must not exist)", dest.Binary)
	if err != nil {
		return err
	}
	dest.Service, err = migrationInput("New service name", dest.Service)
	if err != nil {
		return err
	}
	p, err := co.Plan(ctx, cfg, conn, dest)
	if err != nil {
		return err
	}
	migrate.WritePlan(os.Stderr, p)
	return nil
}

func MigrationMenu(cfg config.Config) error {
	co := migrate.New()
	for {
		action, err := Select("MIGRATE TO A NEW DEVICE:", []huh.Option[string]{
			huh.NewOption("Preview a new migration (no downtime)", "plan"),
			huh.NewOption("Show progress and both node states", "status"),
			huh.NewOption("Transfer or resume the entire data directory", "transfer"),
			huh.NewOption("Confirm handoff and start the replacement", "handoff"),
			huh.NewOption("Return to the original server", "return"),
			huh.NewOption("Discard an untouched preview", "cancel"),
			huh.NewOption("Back", "back"),
		})
		if err != nil {
			return err
		}
		if action == "back" {
			return nil
		}
		err = migrationAction(cfg, co, action)
		if err != nil && !errors.Is(err, ErrCancelled) {
			fmt.Fprintln(os.Stderr, err)
		}
	}
}

func migrationAction(cfg config.Config, co *migrate.Coordinator, action string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if action == "plan" {
		return migrationPlan(ctx, cfg, co)
	}
	if action == "status" {
		r, err := co.Inspect(ctx)
		if err != nil {
			return err
		}
		migrate.WriteStatus(os.Stderr, r)
		return nil
	}
	s, err := co.Local.Status()
	if err != nil {
		return err
	}
	migrate.WriteState(os.Stderr, s)
	prompt := map[string]string{
		"transfer": "Create recovery files, stop both nodes, and copy the entire data directory? Both stay stopped until handoff.",
		"handoff":  "Start the replacement server? The original will remain blocked, including after reboot.",
		"return":   "Stop and block the replacement, then restart the original with its retained data?",
		"cancel":   "Discard this preview? This is allowed only before any node changes.",
	}[action]
	ok, err := Confirm(prompt)
	if err != nil {
		return err
	}
	if !ok {
		return ErrCancelled
	}
	if action == "cancel" {
		return co.CancelPlan(ctx)
	}
	var result migrate.State
	switch action {
	case "transfer":
		pass := os.Getenv("NOMCTL_WALLET_PASSPHRASE")
		if s.Phase == "planned" && pass == "" {
			pass, err = WalletPassphrase(true)
			if err != nil {
				return err
			}
		}
		result, err = co.Copy(ctx, pass, os.Stderr)
	case "handoff":
		result, err = co.Handoff(ctx)
	case "return":
		result, err = co.Return(ctx)
	}
	if result.Plan.ID != "" {
		migrate.WriteState(os.Stderr, result)
	}
	if err == nil {
		fmt.Fprintln(os.Stderr, migrate.FollowUp)
	}
	return err
}
