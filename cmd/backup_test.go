package cmd

import (
	"testing"

	"github.com/0x3639/nomctl/internal/config"
)

func TestApplyBackupFlagsPrecedence(t *testing.T) {
	cfg = config.Default()
	cfg.MaxBackups = 0 // invalid environment value
	cmd := backupCmd
	if err := cmd.Flags().Parse([]string{"--max-backups", "3", "--cadence", "7", "--hour", "5"}); err != nil {
		t.Fatal(err)
	}
	if err := applyBackupFlags(cmd); err != nil {
		t.Fatalf("flag must override invalid env value: %v", err)
	}
	if cfg.MaxBackups != 3 || cfg.BackupCadenceDays != 7 || cfg.BackupHour != 5 {
		t.Errorf("flags not applied: %+v", cfg)
	}

	if err := cmd.Flags().Set("max-backups", "31"); err != nil {
		t.Fatal(err)
	}
	if err := applyBackupFlags(cmd); err == nil {
		t.Error("31 backups must be rejected")
	}
	if err := cmd.Flags().Set("max-backups", "5"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Flags().Set("cadence", "366"); err != nil {
		t.Fatal(err)
	}
	if err := applyBackupFlags(cmd); err == nil {
		t.Error("366 day cadence must be rejected")
	}
}

func TestPrivilegedCommandsAreAnnotated(t *testing.T) {
	for _, c := range []string{"deploy", "start", "stop", "restart", "logs", "resync", "backup", "restore"} {
		sub, _, err := rootCmd.Find([]string{c})
		if err != nil || sub.Annotations[annotationRoot] != "true" {
			t.Errorf("%s should require root", c)
		}
	}
	for _, c := range []string{"env"} {
		sub, _, err := rootCmd.Find([]string{c})
		if err != nil || sub.Annotations[annotationRoot] == "true" {
			t.Errorf("%s must not require root", c)
		}
	}
}
