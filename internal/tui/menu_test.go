package tui

import (
	"testing"

	"github.com/0x3639/nomctl/internal/config"
)

func TestMenuOptions(t *testing.T) {
	opts := MenuOptions()
	want := []string{"Manage node", "Backups & recovery", "Install & upgrade", "Pillar & orchestrator", "Alerts & tools", "Exit"}
	if len(opts) != len(want) {
		t.Fatalf("unexpected options: %+v", opts)
	}
	for i, label := range want {
		if opts[i].Key != label {
			t.Errorf("label %d = %q", i, opts[i].Key)
		}
	}
}

func TestRequestedLabelsAndReachableActions(t *testing.T) {
	labels := map[Action]string{
		ActionBackup: "Backup chain data locally", ActionRestore: "Restore local chain data",
		ActionBootstrap: `Restore from "trusted" bootstrap snapshot`,
		ActionDeploy:    "Deploy a node from source", ActionUpgradeSource: "Upgrade a node from source",
		ActionUpgradeNode: "Upgrade a node from signed release",
	}
	seen := map[Action]bool{}
	for group, menu := range menus {
		if group == "" {
			continue
		}
		entries := menuEntries(group)
		if entries[len(entries)-1].action != ActionBack {
			t.Errorf("%s has no Back", group)
		}
		for _, entry := range menu.entries {
			if seen[entry.action] {
				t.Errorf("duplicate action %s", entry.action)
			}
			seen[entry.action] = true
			if expected, ok := labels[entry.action]; ok && entry.label != expected {
				t.Errorf("%s: %q", entry.action, entry.label)
			}
		}
	}
	for action := range labels {
		if !seen[action] {
			t.Errorf("missing action %s", action)
		}
	}
}

func TestSubmenuNavigation(t *testing.T) {
	steps := []struct {
		menu, previous, choice Action
		err                    error
	}{
		{"", "", menuBackups, nil},
		{menuBackups, "", ActionBackup, nil},
		{menuBackups, ActionBackup, ActionRestore, nil},
		{menuBackups, ActionRestore, ActionBack, nil},
		{"", menuBackups, menuInstall, nil},
		{menuInstall, "", "", ErrCancelled},
		{"", menuInstall, ActionExit, nil},
	}
	index := 0
	choose := func(menu, previous Action) (Action, error) {
		if index >= len(steps) {
			t.Fatal("menu did not exit")
		}
		step := steps[index]
		index++
		if menu != step.menu || previous != step.previous {
			t.Fatalf("step %d: menu=%s previous=%s", index, menu, previous)
		}
		return step.choice, step.err
	}
	var calls []Action
	dispatch := func(_ *config.Config, a Action) error { calls = append(calls, a); return ErrCancelled }
	cfg := config.Default()
	if err := runMenus(&cfg, choose, dispatch); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || calls[0] != ActionBackup || calls[1] != ActionRestore {
		t.Fatal(calls)
	}
}

func TestParsers(t *testing.T) {
	for in, want := range map[string]int{"1": 1, "30": 30, "7": 7} {
		if n, ok := ParseMaxBackups(in); !ok || n != want {
			t.Errorf("ParseMaxBackups(%q) = %d,%v", in, n, ok)
		}
	}
	for _, in := range []string{"0", "31", "abc", "", "100"} {
		if _, ok := ParseMaxBackups(in); ok {
			t.Errorf("ParseMaxBackups(%q) should be invalid", in)
		}
	}
	if n, ok := ParseCadenceDays("365"); !ok || n != 365 {
		t.Error("365 days should be valid")
	}
	if _, ok := ParseCadenceDays("366"); ok {
		t.Error("366 days should be invalid")
	}
	for in, want := range map[string]int{"0": 0, "9": 9, "10": 10, "23": 23} {
		if n, ok := ParseHour(in); !ok || n != want {
			t.Errorf("ParseHour(%q) = %d,%v", in, n, ok)
		}
	}
	for _, in := range []string{"24", "-1", "x", ""} {
		if _, ok := ParseHour(in); ok {
			t.Errorf("ParseHour(%q) should be invalid", in)
		}
	}
}

func TestDispatchUnknown(t *testing.T) {
	cfg := config.Default()
	if err := Dispatch(&cfg, Action("bogus")); err == nil {
		t.Error("unknown action should error")
	}
}
