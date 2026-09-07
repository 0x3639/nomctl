package cmd

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/0x3639/nomctl/internal/execx"
	"github.com/0x3639/nomctl/internal/logx"
	"github.com/0x3639/nomctl/internal/nodeconfig"
	"github.com/0x3639/nomctl/internal/producer"
	"github.com/0x3639/nomctl/internal/service"
	"github.com/0x3639/nomctl/internal/tui"
	"github.com/0x3639/nomctl/internal/ui"
)

var (
	flagConfigRestart     bool
	flagConfigForce       bool
	flagConfigShowSecrets bool
)

var nodeConfigCmd = &cobra.Command{
	Use:   "config",
	Short: "Show and edit the node's config.json with validation",
	Long: `Reads and writes ` + "<data dir>/config.json" + ` against go-zenon's schema, so a
mistyped key or an out-of-range value is refused instead of silently
ignored by znnd. Keys are dotted: RPC.HTTPPort, Net.MaxPeers, LogLevel.
Every write keeps the previous file as config.json.bak.<unix>. The node
reads the file at start: pass --restart or restart it yourself.`,
}

var nodeConfigShowCmd = &cobra.Command{
	Use:         "show",
	Short:       "Every setting with its value and whether it is set in the file or a default",
	Args:        cobra.NoArgs,
	Annotations: diagnostic(),
	RunE: func(cmd *cobra.Command, _ []string) error {
		d, err := nodeconfig.Load(producer.ConfigPath(cfg.ZnnDir))
		if err != nil {
			return err
		}
		showConfig(cmd.OutOrStdout(), d, flagConfigShowSecrets)
		if err := nodeconfig.ValidateStrict(d); err != nil {
			fmt.Fprintf(cmd.OutOrStdout(), "\nProblems:\n%s\n", indent(err.Error()))
		}
		return nil
	},
}

func showConfig(out io.Writer, d *nodeconfig.Document, showSecrets bool) {
	known := map[string]bool{}
	for _, s := range nodeconfig.Schema {
		known[s.Key] = true
		v, fromFile := nodeconfig.Effective(d, s)
		if s.Reserved != "" && !fromFile {
			continue
		}
		text := nodeconfig.Format(v)
		if s.Key == "Producer.Password" && !showSecrets {
			text = "******** (--show-secrets)"
		}
		source := "default"
		if fromFile {
			source = "file"
		}
		fmt.Fprintf(out, "%-22s %-8s %s\n", s.Key, source, text)
	}
	for _, key := range d.Keys() {
		if !known[key] {
			v, _, _ := d.Get(key)
			fmt.Fprintf(out, "%-22s %-8s %s   (unknown to znnd)\n", key, "file", nodeconfig.Format(v))
		}
	}
}

func indent(s string) string {
	return "  " + strings.ReplaceAll(strings.TrimSpace(s), "\n", "\n  ")
}

var nodeConfigGetCmd = &cobra.Command{
	Use:         "get KEY",
	Short:       "Print one setting's effective value",
	Args:        cobra.ExactArgs(1),
	Annotations: diagnostic(),
	RunE: func(cmd *cobra.Command, args []string) error {
		d, err := nodeconfig.Load(producer.ConfigPath(cfg.ZnnDir))
		if err != nil {
			return err
		}
		s, ok := nodeconfig.Lookup(args[0])
		if !ok {
			v, present, err := d.Get(args[0])
			if err != nil {
				return err
			}
			if !present {
				return fmt.Errorf("unknown key %s; see: nomctl config show", args[0])
			}
			fmt.Fprintln(cmd.OutOrStdout(), nodeconfig.Format(v))
			return nil
		}
		v, _ := nodeconfig.Effective(d, s)
		if s.Key == "Producer.Password" && !flagConfigShowSecrets {
			return errors.New("the producer password is printed only with --show-secrets")
		}
		fmt.Fprintln(cmd.OutOrStdout(), nodeconfig.Format(v))
		return nil
	},
}

var nodeConfigSetCmd = &cobra.Command{
	Use:   "set KEY VALUE",
	Short: "Set one setting (typed and validated) and back up the previous file",
	Long: `Values are parsed by the key's type: true/false, integers, strings, and
lists as a comma-separated string or a JSON array. Producer.* keys belong
to "nomctl pillar setup" and are refused. Unknown keys are refused unless
--force, because znnd would ignore them without a word.`,
	Args:        cobra.ExactArgs(2),
	Annotations: rootOnly(),
	RunE: func(cmd *cobra.Command, args []string) error {
		return withLock("config", func() error {
			backup, err := setConfigValue(cfg.ZnnDir, args[0], args[1], flagConfigForce)
			if err != nil {
				return err
			}
			reportConfigWrite(cmd.OutOrStdout(), backup)
			return restartIfAsked(cmd.OutOrStdout())
		})
	},
}

// setConfigValue applies one typed change and saves.
func setConfigValue(znnDir, key, text string, force bool) (backup string, err error) {
	path := producer.ConfigPath(znnDir)
	d, err := nodeconfig.Load(path)
	if err != nil {
		return "", err
	}
	s, known := nodeconfig.Lookup(key)
	var value any
	switch {
	case known && s.Reserved != "":
		return "", fmt.Errorf("%s is managed by: sudo nomctl %s", key, s.Reserved)
	case known:
		if value, err = nodeconfig.ParseValue(s, text); err != nil {
			return "", err
		}
	case force:
		value = text
	default:
		return "", fmt.Errorf("unknown key %s (znnd would ignore it); --force writes it anyway", key)
	}
	if err := d.Set(key, value); err != nil {
		return "", err
	}
	if force {
		return nodeconfig.SaveUnchecked(path, d, time.Now())
	}
	return nodeconfig.Save(path, d, time.Now())
}

var nodeConfigUnsetCmd = &cobra.Command{
	Use:         "unset KEY",
	Short:       "Remove a setting so go-zenon's default applies",
	Args:        cobra.ExactArgs(1),
	Annotations: rootOnly(),
	RunE: func(cmd *cobra.Command, args []string) error {
		return withLock("config", func() error {
			if s, ok := nodeconfig.Lookup(args[0]); ok && s.Reserved != "" {
				return fmt.Errorf("%s is managed by: sudo nomctl %s", args[0], s.Reserved)
			}
			path := producer.ConfigPath(cfg.ZnnDir)
			d, err := nodeconfig.Load(path)
			if err != nil {
				return err
			}
			if _, present, _ := d.Get(args[0]); !present {
				fmt.Fprintf(cmd.OutOrStdout(), "%s is not set; nothing to do\n", args[0])
				return nil
			}
			if err := d.Unset(args[0]); err != nil {
				return err
			}
			backup, err := nodeconfig.Save(path, d, time.Now())
			if err != nil {
				return err
			}
			reportConfigWrite(cmd.OutOrStdout(), backup)
			return restartIfAsked(cmd.OutOrStdout())
		})
	},
}

var nodeConfigEditCmd = &cobra.Command{
	Use:   "edit",
	Short: "Open config.json in $EDITOR; the result must validate before it is installed",
	Long: `Opens a copy of config.json in $VISUAL, $EDITOR or nano. When the editor
exits the copy must parse as JSON and pass validation; otherwise you can
reopen it with the problems shown, or abort with the original untouched.`,
	Args:        cobra.NoArgs,
	Annotations: rootOnly(),
	RunE: func(cmd *cobra.Command, _ []string) error {
		if !ui.Interactive() {
			return errors.New("config edit needs a terminal; use: nomctl config set KEY VALUE")
		}
		return withLock("config", func() error {
			outcome, backup, err := nodeconfig.Edit(producer.ConfigPath(cfg.ZnnDir), runEditor, tui.RetryEdit, time.Now())
			if err != nil {
				return err
			}
			switch outcome {
			case nodeconfig.EditUnchanged:
				fmt.Fprintln(cmd.OutOrStdout(), "No changes.")
				return nil
			case nodeconfig.EditAborted:
				fmt.Fprintln(cmd.OutOrStdout(), "Aborted; config.json is unchanged.")
				return nil
			}
			reportConfigWrite(cmd.OutOrStdout(), backup)
			return restartIfAsked(cmd.OutOrStdout())
		})
	},
}

// runEditor runs the user's editor on path in the foreground.
func runEditor(path string) error {
	cmdline := nodeconfig.EditorCommand()
	return execx.New(cmdline[0], append(cmdline[1:], path)...).Interactive()
}

func reportConfigWrite(out io.Writer, backup string) {
	logx.Success("config.json written")
	if backup != "" {
		fmt.Fprintf(out, "Previous file kept as %s\n", backup)
	}
	if d, err := nodeconfig.Load(producer.ConfigPath(cfg.ZnnDir)); err == nil {
		if unknown := nodeconfig.UnknownKeys(d); len(unknown) > 0 {
			fmt.Fprintf(out, "Note: znnd ignores these keys: %s (nomctl config unset KEY removes one)\n", strings.Join(unknown, ", "))
		}
	}
}

// restartIfAsked restarts the node with --restart, else says it is needed.
func restartIfAsked(out io.Writer) error {
	if !flagConfigRestart {
		if service.IsActive(cfg.ServiceName) {
			fmt.Fprintf(out, "%s reads config.json at start: sudo nomctl restart (or pass --restart)\n", cfg.ServiceName)
		}
		return nil
	}
	return service.Restart(cfg.ServiceName)
}

func init() {
	tui.EditorRunner = runEditor
	for _, c := range []*cobra.Command{nodeConfigSetCmd, nodeConfigUnsetCmd, nodeConfigEditCmd} {
		c.Flags().BoolVar(&flagConfigRestart, "restart", false, "restart the node after writing")
	}
	nodeConfigSetCmd.Flags().BoolVar(&flagConfigForce, "force", false, "write an unknown key or skip validation")
	nodeConfigShowCmd.Flags().BoolVar(&flagConfigShowSecrets, "show-secrets", false, "print the producer password")
	nodeConfigGetCmd.Flags().BoolVar(&flagConfigShowSecrets, "show-secrets", false, "allow printing the producer password")
	nodeConfigCmd.AddCommand(nodeConfigShowCmd, nodeConfigGetCmd, nodeConfigSetCmd, nodeConfigUnsetCmd, nodeConfigEditCmd)
	rootCmd.AddCommand(nodeConfigCmd)
}
