package cmd

import "testing"

func TestDiagnosticCommandsSkipPreflight(t *testing.T) {
	for _, c := range diagnosticCommands {
		sub, _, err := rootCmd.Find([]string{c})
		if err != nil || sub.Name() != c {
			t.Fatalf("%s not registered: %v", c, err)
		}
		if sub.Annotations[annotationRoot] != "true" || sub.Annotations[annotationNoPreflight] != "true" {
			t.Errorf("%s must require root and skip preflight: %v", c, sub.Annotations)
		}
	}
}

// diagnosticCommands grows as the commands are added.
var diagnosticCommands = []string{"status", "top", "support-bundle", "upgrade", "start", "stop", "restart", "logs"}

func TestAlertsCommands(t *testing.T) {
	for _, c := range []string{"setup", "probe", "status", "list", "test"} {
		sub, _, err := rootCmd.Find([]string{"alerts", c})
		if err != nil || sub.Name() != c {
			t.Fatalf("alerts %s not registered: %v", c, err)
		}
		if sub.Annotations[annotationRoot] != "true" || sub.Annotations[annotationNoPreflight] != "true" {
			t.Errorf("alerts %s must be root + no preflight: %v", c, sub.Annotations)
		}
	}
	// The daemon is started by systemd as the unprivileged run user.
	run, _, err := rootCmd.Find([]string{"alerts", "run"})
	if err != nil || run.Annotations[annotationRoot] == "true" || run.Annotations[annotationNoPreflight] != "true" {
		t.Errorf("alerts run must be unprivileged + no preflight: %v", run.Annotations)
	}
	for _, c := range []string{"enable", "disable", "set", "unpair"} {
		sub, _, err := rootCmd.Find([]string{"alerts", c})
		if err != nil || sub.Name() != c || sub.Annotations[annotationRoot] != "true" {
			t.Errorf("alerts %s must require root: %v", c, err)
		}
	}
}

func TestOrchestratorLogsSkipPreflight(t *testing.T) {
	if orchestratorLogsCmd.Annotations[annotationRoot] != "true" || orchestratorLogsCmd.Annotations[annotationNoPreflight] != "true" {
		t.Fatal("orchestrator logs must require root and skip preflight")
	}
}

func TestMenuEntrySkipsPreflight(t *testing.T) {
	if rootCmd.Annotations[annotationRoot] != "true" || rootCmd.Annotations[annotationNoPreflight] != "true" {
		t.Fatal("opening the menu must require root without running setup preflight")
	}
}
