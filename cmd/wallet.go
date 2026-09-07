package cmd

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/0x3639/nomctl/internal/alerts"
	"github.com/0x3639/nomctl/internal/logx"
	"github.com/0x3639/nomctl/internal/service"
	"github.com/0x3639/nomctl/internal/tui"
	"github.com/0x3639/nomctl/internal/ui"
	"github.com/0x3639/nomctl/internal/walletbackup"
)

var (
	flagWalletOutput    string
	flagWalletNoEncrypt bool
	flagWalletRestart   bool
)

// PassphraseEnv supplies the wallet backup passphrase to scripts.
const PassphraseEnv = "NOMCTL_WALLET_PASSPHRASE"

var backupWalletCmd = &cobra.Command{
	Use:   "wallet",
	Short: "Archive wallet/ and config.json, encrypted with a passphrase (age)",
	Long: `Writes a small archive of the wallet directory (every key file, including
the producer key) and config.json (which holds the producer password) to
<backup dir>/wallet/, encrypted with age using a passphrase, plus a .sha256
sidecar. Chain-data backups leave these files out, and the chain-data
retention rule never prunes wallet archives.

The passphrase is prompted with hidden input, or read from ` + PassphraseEnv + `
in scripts. Any machine with the age tool can open the archive:
  age -d -o wallet.tar.gz <archive>.tar.gz.age

--no-encrypt writes a plain archive; it contains the producer password.`,
	Args:        cobra.NoArgs,
	Annotations: rootOnly(),
	RunE: func(cmd *cobra.Command, _ []string) error {
		opts := walletbackup.Options{Output: flagWalletOutput, Plain: flagWalletNoEncrypt}
		if !opts.Plain {
			p, err := walletPassphrase(true)
			if err != nil {
				return err
			}
			opts.Passphrase = p
		} else {
			slog.Warn("--no-encrypt: the archive holds the producer password in clear; store it somewhere encrypted")
		}
		var res walletbackup.Result
		err := withLock("backup wallet", func() error {
			var err error
			res, err = walletbackup.Create(cfg, opts)
			return err
		})
		if err != nil {
			return err
		}
		logx.Success("Wallet backup written")
		out := cmd.OutOrStdout()
		fmt.Fprintf(out, "%-10s %s\n%-10s %s\n%-10s %s\n", "Archive", res.Path, "Checksum", res.HashPath, "Contains", strings.Join(res.Files, ", "))
		if res.Encrypted {
			fmt.Fprintln(out, "Keep the passphrase with the archive's copy: without it the backup cannot be opened.")
		}
		fmt.Fprintln(out, "Copy the archive off this machine; a backup on the node's own disk does not survive the node.")
		return nil
	},
}

var restoreWalletCmd = &cobra.Command{
	Use:   "wallet FILE",
	Short: "Restore wallet/ and config.json from a wallet backup",
	Long: `Opens the archive (decrypting with the passphrase for .age files), checks
that it holds only wallet files and config.json, and, when its config names
a producer key, that the key is present, opens with the archived password
and matches the archived address. The current wallet directory and
config.json are moved to <backup dir>/restore/wallet-safety.<unix>/ before
the archived files are put in place. The node reads both at start:
--restart restarts it, otherwise restart it yourself.`,
	Args:        cobra.ExactArgs(1),
	Annotations: rootOnly(),
	RunE: func(cmd *cobra.Command, args []string) error {
		passphrase := ""
		if strings.HasSuffix(args[0], ".age") {
			p, err := walletPassphrase(false)
			if err != nil {
				return err
			}
			passphrase = p
		}
		archive, err := walletbackup.Open(args[0], passphrase)
		if err != nil {
			return err
		}
		var res walletbackup.RestoreResult
		err = withLock("restore wallet", func() error {
			var err error
			res, err = walletbackup.Restore(cfg, archive, flagWalletRestart, time.Now())
			return err
		})
		if err != nil {
			if res.SafetyDir != "" {
				fmt.Fprintf(os.Stderr, "Previous wallet files are under %s\n", res.SafetyDir)
			}
			return err
		}
		logx.Success("Wallet backup restored: " + strings.Join(res.Files, ", "))
		out := cmd.OutOrStdout()
		if res.Address != "" {
			fmt.Fprintf(out, "%-18s %s\n", "Producer address", res.Address)
		}
		fmt.Fprintf(out, "%-18s %s\n", "Previous files", res.SafetyDir)
		switch {
		case res.Restarted:
		case res.WasRunning:
			fmt.Fprintf(out, "%s reads these files at start: sudo nomctl restart\n", cfg.ServiceName)
		}
		return nil
	},
}

// walletPassphrase reads the passphrase from the environment or the
// terminal; when creating, it is asked twice.
func walletPassphrase(confirm bool) (string, error) {
	if p := os.Getenv(PassphraseEnv); p != "" {
		return p, nil
	}
	if !ui.Interactive() {
		return "", errors.New("no terminal: set " + PassphraseEnv)
	}
	return tui.WalletPassphrase(confirm)
}

// walletChecker feeds the wallet_backup_missing rule.
func walletChecker() alerts.WalletInfo {
	s := walletbackup.Check(cfg)
	return alerts.WalletInfo{ProducerConfigured: s.ProducerConfigured, Missing: s.Missing(), NewestBackup: s.NewestBackup}
}

func init() {
	walletbackup.SetServiceHooks(service.IsActive, service.Restart)
	backupWalletCmd.Flags().StringVar(&flagWalletOutput, "output", "", "directory to write to (default <backup dir>/wallet)")
	backupWalletCmd.Flags().BoolVar(&flagWalletNoEncrypt, "no-encrypt", false, "write a plain archive (it holds the producer password)")
	restoreWalletCmd.Flags().BoolVar(&flagWalletRestart, "restart", false, "restart the node after restoring")
	backupCmd.AddCommand(backupWalletCmd)
	restoreCmd.AddCommand(restoreWalletCmd)
}
