package producer

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/deploy"
	"github.com/0x3639/nomctl/internal/logx"
	"github.com/0x3639/nomctl/internal/service"
)

// Prompts are the questions Setup asks; the CLI answers them from flags,
// the menu from the terminal. A nil field means "not interactive": keep an
// existing configuration, and fail when a password is needed but none was
// given.
type Prompts struct {
	// KeepExisting asks whether to keep a producer configuration already in
	// config.json (address shown). Default yes.
	KeepExisting func(address string) (bool, error)
	// PasswordFor asks for the password of an existing key file; attempt is
	// 1-based, of three.
	PasswordFor func(path string, attempt int) (string, error)
}

// Options for Setup.
type Options struct {
	// Password to use instead of generating one. For an existing key file
	// it is the password to verify.
	Password string
	Prompts  Prompts
	Now      func() time.Time
}

// Result describes what Setup did.
type Result struct {
	Address string
	KeyFile string
	// Password is set only when a key file was created now, so the caller
	// can print it once.
	Password string
	// Created is true for a new key file, Configured when an existing key
	// file was wired in, neither when the existing configuration was kept.
	Created, Configured bool
	ConfigBackup        string
	Restarted           bool
}

// Hooks so tests can stub the host.
var (
	isActive = service.IsActive
	restart  = service.Restart
)

// Setup ensures the node has a producer key configured: keep what is there,
// configure an existing key file, or create a new one; then write
// config.json and restart the node if it is running.
func Setup(cfg config.Config, opts Options) (Result, error) {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	keyPath := KeyFilePath(cfg.ZnnDir)
	cfgPath := ConfigPath(cfg.ZnnDir)
	res := Result{KeyFile: keyPath}

	existing, err := ReadConfig(cfgPath)
	if err != nil {
		return res, err
	}
	keyExists := fileExists(keyPath)
	if existing != nil && existing.Address != "" && keyExists && existing.KeyFilePath == KeyFileName {
		keep := true
		if opts.Prompts.KeepExisting != nil {
			if keep, err = opts.Prompts.KeepExisting(existing.Address); err != nil {
				return res, err
			}
		}
		if keep {
			res.Address = existing.Address
			return res, nil
		}
	}

	var pc Config
	switch {
	case keyExists:
		password, addr, err := unlockExisting(keyPath, opts)
		if err != nil {
			return res, err
		}
		pc = Config{Index: 0, KeyFilePath: KeyFileName, Password: password, Address: addr}
		res.Configured = true
		slog.Info("Using the existing producer key file " + keyPath)
	default:
		password := opts.Password
		if password == "" {
			if password, err = GeneratePassword(); err != nil {
				return res, err
			}
		}
		addr, err := Create(keyPath, password)
		if err != nil {
			return res, err
		}
		pc = Config{Index: 0, KeyFilePath: KeyFileName, Password: password, Address: addr}
		res.Created, res.Password = true, password
		logx.Success("Producer key file created: " + keyPath)
	}
	res.Address = pc.Address

	backup, err := WriteConfig(cfgPath, pc, opts.Now())
	if err != nil {
		return res, err
	}
	res.ConfigBackup = backup
	logx.Success("Producer configured in " + cfgPath)

	if isActive(cfg.ServiceName) {
		slog.Info("Restarting " + cfg.ServiceName + " so it loads the producer key…")
		if err := restart(cfg.ServiceName); err != nil {
			return res, err
		}
		res.Restarted = true
	}
	return res, nil
}

func unlockExisting(keyPath string, opts Options) (password, addr string, err error) {
	if opts.Password != "" {
		addr, err = Verify(keyPath, opts.Password)
		return opts.Password, addr, err
	}
	if opts.Prompts.PasswordFor == nil {
		return "", "", fmt.Errorf("%s exists but is not configured; pass --password to configure it", keyPath)
	}
	for attempt := 1; attempt <= 3; attempt++ {
		password, err = opts.Prompts.PasswordFor(keyPath, attempt)
		if err != nil {
			return "", "", err
		}
		addr, err = Verify(keyPath, strings.TrimSpace(password))
		if err == nil {
			return strings.TrimSpace(password), addr, nil
		}
		if !errors.Is(err, ErrWrongPassword) {
			return "", "", err
		}
		slog.Warn(fmt.Sprintf("wrong password, %d attempt(s) left", 3-attempt))
	}
	return "", "", errors.New("password verification failed three times; aborting")
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// Deploy is the one-step Pillar deployment: build the official go-zenon
// master, install and start the service, then Setup the producer key. A
// node that is already deployed is rebuilt from master and restarted; an
// existing producer configuration is kept (Setup asks when interactive).
func Deploy(cfg config.Config, opts Options) (Result, error) {
	if err := deployRun(cfg, deploy.OfficialRepoURL, deploy.OfficialBranch); err != nil {
		return Result{}, err
	}
	return Setup(cfg, opts)
}

// deployRun is a hook for tests.
var deployRun = deploy.Run

// NextSteps is the text shown after a successful setup.
func NextSteps(address string) string {
	return "Set " + address + " as the producer address of your Pillar (Syrius or znn-cli).\n" +
		"One producer address serves one Pillar. Back up wallet/producer and its password: without them the node cannot produce."
}
