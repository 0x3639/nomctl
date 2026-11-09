package cmd

import (
	"testing"

	"github.com/0x3639/nomctl/internal/config"
	"github.com/spf13/pflag"
)

func TestNodeUpgradeFlags(t *testing.T) {
	oldCfg := cfg
	flags := upgradeNodeCmd.Flags()
	type flagState struct {
		flag    *pflag.Flag
		value   string
		changed bool
	}
	var saved []flagState
	flags.VisitAll(func(f *pflag.Flag) { saved = append(saved, flagState{f, f.Value.String(), f.Changed}) })
	t.Cleanup(func() {
		cfg = oldCfg
		for _, state := range saved {
			if err := state.flag.Value.Set(state.value); err != nil {
				t.Error(err)
			}
			state.flag.Changed = state.changed
		}
	})
	for _, tc := range []struct {
		name            string
		args            []string
		signed, wantErr bool
	}{
		{"configured source by default", nil, false, false},
		{"source point days", []string{"--restore-point-keep", "7"}, false, false},
		{"signed point indefinite", []string{"--signed", "--restore-point-keep", "indefinite"}, true, false},
		{"invalid point days", []string{"--restore-point-keep", "31"}, false, true},
		{"invalid signed point days", []string{"--signed", "--restore-point-keep", "0"}, true, true},
		{"unattended source", []string{"--yes"}, false, false},
		{"source overrides", []string{"--repo", "https://example.invalid/chosen.git", "--branch", "next"}, false, false},
		{"signed picker", []string{"--signed"}, true, false},
		{"signed version", []string{"--signed", "--version", "v1"}, true, false},
		{"unattended signed", []string{"--signed", "--yes"}, true, false},
		{"version requires signed", []string{"--version", "v1"}, false, true},
		{"empty version requires signed", []string{"--version", ""}, false, true},
		{"signed disabled with version", []string{"--signed=false", "--version", "v1"}, false, true},
		{"repo with signed", []string{"--signed", "--repo", "https://example.invalid/chosen.git"}, true, true},
		{"branch with signed", []string{"--signed", "--branch", "next"}, true, true},
		{"empty repo", []string{"--repo", ""}, false, true},
		{"empty branch", []string{"--branch", " "}, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			flags.VisitAll(func(f *pflag.Flag) {
				if err := f.Value.Set(f.DefValue); err != nil {
					t.Fatal(err)
				}
				f.Changed = false
			})
			cfg = config.Default()
			cfg.RepoURL = "https://example.invalid/default.git"
			cfg.BranchName = "configured"
			if err := flags.Parse(tc.args); err != nil {
				t.Fatal(err)
			}
			if flagNodeUpgradeSigned != tc.signed {
				t.Fatalf("signed=%v, want %v", flagNodeUpgradeSigned, tc.signed)
			}
			if err := applyNodeUpgradeFlags(upgradeNodeCmd); (err != nil) != tc.wantErr {
				t.Fatal(err)
			}
			if tc.name == "source point days" && cfg.RestorePointKeepDays != 7 {
				t.Fatal("upgrade retention flag ignored")
			}
			if tc.name == "signed point indefinite" && cfg.RestorePointKeepDays != 0 {
				t.Fatal("indefinite retention flag ignored")
			}
			if tc.name == "source overrides" {
				if cfg.RepoURL != "https://example.invalid/chosen.git" || cfg.BranchName != "next" {
					t.Fatal("source flags did not override configuration")
				}
			} else if cfg.RepoURL != "https://example.invalid/default.git" || cfg.BranchName != "configured" {
				t.Fatal("configured source changed unexpectedly")
			}
		})
	}
	if flags.Lookup("source") != nil {
		t.Fatal("obsolete --source flag still registered")
	}
}

func TestBackupScheduleCommand(t *testing.T) {
	command, _, err := rootCmd.Find([]string{"backup", "schedule"})
	if err != nil || command != backupScheduleCmd || command.Annotations[annotationRoot] != "true" {
		t.Fatal("schedule command missing or unprivileged", err)
	}
	for _, flag := range []string{"retention-days", "hour"} {
		if command.Flags().Lookup(flag) == nil {
			t.Errorf("missing %s", flag)
		}
	}
}
