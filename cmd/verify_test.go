package cmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0x3639/nomctl/internal/config"
)

func TestVerifyCommandsAndSelections(t *testing.T) {
	for _, args := range [][]string{{"verify"}, {"verify", "status"}, {"verify", "schedule"}, {"verify", "schedule", "pause"}, {"verify", "schedule", "resume"}} {
		c, remaining, err := rootCmd.Find(args)
		if err != nil || len(remaining) > 0 || c.Name() != args[len(args)-1] {
			t.Fatal(args, c, err)
		}
		if c.Annotations[annotationRoot] != "true" || c.Annotations[annotationNoPreflight] != "true" {
			t.Fatal("verification must skip preflight", args)
		}
	}
	previous := cfg
	t.Cleanup(func() { cfg = previous })
	cfg = config.Default()
	cfg.BackupDir = t.TempDir()
	for _, f := range []verifyFlags{{file: "a", point: "p"}, {file: "a", all: true}, {point: "p", all: true}, {point: "../escape"}} {
		if _, err := f.targets(); err == nil {
			t.Fatal("accepted invalid selection", f)
		}
	}
	for _, f := range []verifyFlags{{}, {all: true}, {file: "missing-backup"}, {point: "go-zenon_point_legacy"}} {
		if _, err := f.targets(); err != nil {
			t.Fatal(f, err)
		}
	}
}

func TestVerifyCLIJSONFailureAndNoServiceTools(t *testing.T) {
	oldCfg, oldState, oldLock := cfg, verificationStateDir, operationLockPath
	t.Cleanup(func() { cfg, verificationStateDir, operationLockPath = oldCfg, oldState, oldLock })
	cfg = config.Default()
	cfg.BackupDir = t.TempDir()
	verificationStateDir = t.TempDir()
	operationLockPath = filepath.Join(t.TempDir(), "lock")
	archive := filepath.Join(cfg.BackupDir, "go-zenon_backup_broken.tar.gz")
	if err := os.WriteFile(archive, []byte("bad archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := newVerifyCommand()
	c.SetContext(context.Background())
	var out bytes.Buffer
	c.SetOut(&out)
	if err := c.ParseFlags([]string{"--file", archive, "--json"}); err != nil {
		t.Fatal(err)
	}
	if err := c.RunE(c, nil); err == nil {
		t.Fatal("CLI must return nonzero for failed verification")
	}
	if !strings.Contains(out.String(), `"outcome":"failed"`) {
		t.Fatal(out.String())
	}
	if c.Annotations[annotationOptionalServiceTools] != "true" {
		t.Fatal("verification unnecessarily requires systemctl")
	}
}
