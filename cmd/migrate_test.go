package cmd

import (
	"testing"

	"github.com/0x3639/nomctl/internal/config"
)

func TestMigrationCommandsRequireRootAndExplicitSteps(t *testing.T) {
	c := newMigrateCommand()
	for _, name := range []string{"plan", "status", "transfer", "resume", "handoff", "return", "cancel", "_serve", "_receive"} {
		step, _, err := c.Find([]string{name})
		if err != nil || step == c {
			t.Fatalf("missing %s: %v", name, err)
		}
		if step.Annotations[annotationRoot] != "true" || step.Annotations[annotationNoPreflight] != "true" {
			t.Fatalf("%s has incorrect access rules", name)
		}
		if name == "handoff" || name == "return" || name == "transfer" {
			if step.Flags().Lookup("yes").DefValue != "false" {
				t.Fatalf("%s preconfirmed", name)
			}
		}
	}
	plan, _, _ := c.Find([]string{"plan"})
	if plan.Flags().Lookup("destination-service").DefValue != config.DefaultServiceName {
		t.Fatal("wrong service default")
	}
	receive, _, _ := c.Find([]string{"_receive"})
	if !receive.Hidden || !receive.DisableFlagParsing {
		t.Fatal("receiver must preserve rsync server arguments")
	}
}
