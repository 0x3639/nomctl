package restore

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/0x3639/nomctl/internal/backup"
	"github.com/0x3639/nomctl/internal/config"
)

func TestResolve(t *testing.T) {
	cfg := config.Default()
	if got := Resolve(cfg, "go-zenon_backup_1"); got != "/backup/go-zenon_backup_1.tar.gz" {
		t.Errorf("Resolve bare name = %q", got)
	}
	if got := Resolve(cfg, "go-zenon_backup_1.tar.gz"); got != "/backup/go-zenon_backup_1.tar.gz" {
		t.Errorf("Resolve bare name with suffix = %q", got)
	}
	if got := Resolve(cfg, "/mnt/x.tar.gz"); got != "/mnt/x.tar.gz" {
		t.Errorf("Resolve full path = %q", got)
	}
	if got := Resolve(cfg, "./x.tar.gz"); got != "./x.tar.gz" {
		t.Errorf("Resolve relative path = %q", got)
	}
}

func TestMoveAsideRefusesExistingDestination(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{ZnnDir: filepath.Join(root, "znn"), BackupDir: filepath.Join(root, "backup")}
	if err := os.MkdirAll(filepath.Join(cfg.ZnnDir, "nom"), 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	if err := os.MkdirAll(filepath.Join(cfg.BackupDir, "restore", "nom.bak.1700000000"), 0o755); err != nil {
		t.Fatal(err)
	}
	moved, err := MoveAside(cfg, now, []string{"nom"})
	if err == nil || len(moved) != 0 {
		t.Fatalf("expected refusal, got moved=%v err=%v", moved, err)
	}
	if _, err := os.Stat(filepath.Join(cfg.ZnnDir, "nom")); err != nil {
		t.Error("nom must stay in place")
	}
}

func TestVerify(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "a.tar.gz")
	if err := Verify(archive); err == nil {
		t.Error("missing archive should fail")
	}
	if err := os.WriteFile(archive, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Verify(archive); err == nil {
		t.Error("missing hash should fail")
	}
	sum, _ := backup.SHA256File(archive)
	if err := os.WriteFile(backup.HashPath(archive), []byte(sum+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Verify(archive); err != nil {
		t.Errorf("valid archive failed: %v", err)
	}
	if err := os.WriteFile(backup.HashPath(archive), []byte("deadbeef"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Verify(archive); err == nil {
		t.Error("mismatched hash should fail")
	}
}
