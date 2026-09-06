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
var diagnosticCommands = []string{"status", "top", "support-bundle"}

func TestAlertsCommands(t *testing.T) {
	for _, c := range []string{"setup", "run", "status", "list", "test"} {
		sub, _, err := rootCmd.Find([]string{"alerts", c})
		if err != nil || sub.Name() != c {
			t.Fatalf("alerts %s not registered: %v", c, err)
		}
		if sub.Annotations[annotationRoot] != "true" || sub.Annotations[annotationNoPreflight] != "true" {
			t.Errorf("alerts %s must be root + no preflight: %v", c, sub.Annotations)
		}
	}
	for _, c := range []string{"enable", "disable", "set", "unpair"} {
		sub, _, err := rootCmd.Find([]string{"alerts", c})
		if err != nil || sub.Name() != c || sub.Annotations[annotationRoot] != "true" {
			t.Errorf("alerts %s must require root: %v", c, err)
		}
	}
}
