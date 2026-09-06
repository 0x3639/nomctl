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
var diagnosticCommands = []string{"status", "top"}
