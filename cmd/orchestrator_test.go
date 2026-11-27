package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/orchestrator"
)

func resetFlags(t *testing.T, cmd *cobra.Command) {
	t.Helper()
	type state struct {
		flag    *pflag.Flag
		value   string
		changed bool
	}
	var saved []state
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		saved = append(saved, state{f, f.Value.String(), f.Changed})
		_ = f.Value.Set(f.DefValue)
		f.Changed = false
	})
	t.Cleanup(func() {
		for _, s := range saved {
			_ = s.flag.Value.Set(s.value)
			s.flag.Changed = s.changed
		}
	})
}

func TestOrchestratorCommandsRegisteredAndAnnotated(t *testing.T) {
	want := []struct {
		args        []string
		noPreflight bool
	}{
		{[]string{"orchestrator", "install"}, false},
		{[]string{"orchestrator", "upgrade"}, true},
		{[]string{"orchestrator", "recover"}, true},
		{[]string{"orchestrator", "status"}, true},
		{[]string{"orchestrator", "logs"}, true},
		{[]string{"orchestrator", "backup"}, true},
		{[]string{"orchestrator", "copy-sync"}, true},
		{[]string{"orchestrator", "config", "show"}, true},
		{[]string{"orchestrator", "config", "edit"}, true},
		{[]string{"orchestrator", "template", "list"}, true},
		{[]string{"orchestrator", "template", "apply"}, true},
		{[]string{"orchestrator", "hard-reset"}, false},
	}
	for _, tc := range want {
		sub, rem, err := rootCmd.Find(tc.args)
		if err != nil || len(rem) != 0 || sub.Name() != tc.args[len(tc.args)-1] {
			t.Fatalf("missing command %v: %v", tc.args, err)
		}
		if sub.Annotations[annotationRoot] != "true" {
			t.Errorf("%v must require root", tc.args)
		}
		if (sub.Annotations[annotationNoPreflight] == "true") != tc.noPreflight {
			t.Errorf("%v noPreflight=%q, want %v", tc.args, sub.Annotations[annotationNoPreflight], tc.noPreflight)
		}
	}
}

func TestOrchestratorReleaseRepoFlagPrecedence(t *testing.T) {
	oldCfg := cfg
	t.Cleanup(func() { cfg = oldCfg })
	resetFlags(t, orchestratorInstallCmd)

	cfg = config.Default()
	cfg.OrchestratorRepo = "HyperCore-Team/orchestrator"
	if err := orchestratorInstallCmd.Flags().Parse([]string{"--repo", "https://github.com/0x3639/orchestrator/"}); err != nil {
		t.Fatal(err)
	}
	if err := applyOrchestratorReleaseFlags(orchestratorInstallCmd); err != nil {
		t.Fatal(err)
	}
	if cfg.OrchestratorRepo != "0x3639/orchestrator" {
		t.Fatalf("OrchestratorRepo = %q", cfg.OrchestratorRepo)
	}

	if err := orchestratorInstallCmd.Flags().Set("repo", "https://gitlab.com/evil/orchestrator"); err != nil {
		t.Fatal(err)
	}
	if err := applyOrchestratorReleaseFlags(orchestratorInstallCmd); err == nil {
		t.Fatal("expected non-github repo to be rejected")
	}
}

func TestOrchestratorTemplateFlagsAndList(t *testing.T) {
	var out bytes.Buffer
	orchestratorTemplateListCmd.SetOut(&out)
	t.Cleanup(func() { orchestratorTemplateListCmd.SetOut(nil) })
	if err := orchestratorTemplateListCmd.RunE(orchestratorTemplateListCmd, nil); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"dRPC (drpc.org)", "Alchemy", "BNB Chain", "Ethereum", "100", "0.5"} {
		if !strings.Contains(text, want) {
			t.Errorf("template list missing %q:\n%s", want, text)
		}
	}

	cases := []struct {
		name    string
		args    []string
		wantBNB string
		wantETH string
		wantErr bool
	}{
		{"all drpc", []string{"--provider", "drpc", "--network", "all"}, orchestrator.ProviderDRPC, orchestrator.ProviderDRPC, false},
		{"eth alchemy only", []string{"--provider", "alchemy", "--network", "eth"}, "", orchestrator.ProviderAlchemy, false},
		{"mixed per network", []string{"--bnb", "drpc.org", "--eth", "alchemy"}, orchestrator.ProviderDRPC, orchestrator.ProviderAlchemy, false},
		{"bsc alias", []string{"--bsc", "drpc"}, orchestrator.ProviderDRPC, "", false},
		{"conflict bnb and bsc", []string{"--bnb", "drpc", "--bsc", "alchemy"}, "", "", true},
		{"unknown provider", []string{"--provider", "infura"}, "", "", true},
		{"empty flags", nil, "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetFlags(t, orchestratorTemplateApplyCmd)
			if err := orchestratorTemplateApplyCmd.Flags().Parse(tc.args); err != nil {
				t.Fatal(err)
			}
			got, err := resolveTemplateAssignments(orchestratorTemplateApplyCmd)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v, wantErr=%v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if got[orchestrator.NetworkBNBChain] != tc.wantBNB || got[orchestrator.NetworkEthereum] != tc.wantETH {
				t.Fatalf("got=%v, want BNB=%q ETH=%q", got, tc.wantBNB, tc.wantETH)
			}
		})
	}
}

func TestOrchestratorCopySyncFlagValidation(t *testing.T) {
	oldCfg := cfg
	t.Cleanup(func() { cfg = oldCfg })
	cfg = config.Default()

	resetFlags(t, orchestratorCopySyncCmd)
	if err := applyOrchestratorCopySyncFlags(orchestratorCopySyncCmd); err == nil {
		t.Fatal("missing --to must be rejected")
	}

	if err := orchestratorCopySyncCmd.Flags().Parse([]string{"--to", "root@other-server", "--port", "2222", "--remote-dir", "/srv/orch", "--remote-service", "orchestrator"}); err != nil {
		t.Fatal(err)
	}
	if err := applyOrchestratorCopySyncFlags(orchestratorCopySyncCmd); err != nil {
		t.Fatalf("valid copy-sync flags rejected: %v", err)
	}
}
