package restore

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0x3639/nomctl/internal/backup"
	"github.com/0x3639/nomctl/internal/config"
)

func TestRestorePointRestoresMatchingPairOrRollsBack(t *testing.T) {
	for _, mode := range []string{"success", "binary-install-failure", "start-failure", "bad-binary", "bad-archive", "no-space", "data-only", "data-only-missing-binary", "data-only-bad-archive", "data-only-no-space", "data-only-incomplete"} {
		t.Run(mode, func(t *testing.T) {
			dataOnly := strings.HasPrefix(mode, "data-only")
			success := mode == "success" || mode == "data-only" || mode == "data-only-missing-binary"
			cfg := config.Default()
			cfg.ZnnDir = t.TempDir()
			cfg.BackupDir = t.TempDir()
			cfg.InstallDir = t.TempDir()
			cfg.MinFreeSpaceKB = 0
			// Keep the real-filesystem integration path, but do not fail its
			// setup because unrelated host files exhaust the backup allowance.
			if err := backup.CheckRestoreSpace(cfg, cfg.BackupDir, 1<<20); err != nil {
				t.Skipf("integration test needs backup capacity: %v", err)
			}
			put := func(path, text string) {
				t.Helper()
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			put(cfg.BinaryPath(), "point binary")
			put(filepath.Join(cfg.ZnnDir, "nom", "state"), "point chain")
			put(filepath.Join(cfg.ZnnDir, "config.json"), "keep config")
			put(filepath.Join(cfg.ZnnDir, "wallet", "key"), "keep wallet")
			s, err := backup.PrepareRestorePoint(cfg, "test point")
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Capture(); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Finish(); err != nil {
				t.Fatal(err)
			}
			points, err := backup.ListRestorePoints(cfg)
			if err != nil || len(points) != 1 {
				t.Fatal("missing point", err)
			}
			p := points[0]
			put(cfg.BinaryPath(), "current binary")
			installedBefore, err := os.Stat(cfg.BinaryPath())
			if err != nil {
				t.Fatal(err)
			}
			put(filepath.Join(cfg.ZnnDir, "nom", "state"), "current chain")
			put(filepath.Join(cfg.ZnnDir, "network", "new"), "new network")
			oldStop, oldStart, oldInstall := stopService, startService, installPointBinary
			t.Cleanup(func() { stopService, startService, installPointBinary = oldStop, oldStart, oldInstall })
			stops, starts := 0, 0
			stopService = func(string) error { stops++; return nil }
			startService = func(string) error {
				starts++
				binary, _ := os.ReadFile(cfg.BinaryPath())
				data, _ := os.ReadFile(filepath.Join(cfg.ZnnDir, "nom", "state"))
				wantBinary, wantData := "point binary", "point chain"
				if dataOnly {
					wantBinary = "current binary"
				}
				if mode == "binary-install-failure" {
					wantBinary, wantData = "current binary", "current chain"
				}
				if string(binary) != wantBinary || string(data) != wantData {
					t.Fatalf("restart with mismatched pair: %q / %q", binary, data)
				}
				if mode == "start-failure" {
					return errors.New("start failed")
				}
				return nil
			}
			switch mode {
			case "binary-install-failure":
				installPointBinary = func(string, string) error { return errors.New("install failed") }
			case "bad-binary":
				put(p.BinaryPath(), "tampered")
			case "bad-archive", "data-only-bad-archive":
				put(p.ArchivePath(), "tampered")
			case "no-space", "data-only-no-space":
				cfg.MinFreeSpaceKB = 1 << 62
			case "data-only-missing-binary":
				if err := os.Remove(p.BinaryPath()); err != nil {
					t.Fatal(err)
				}
			case "data-only-incomplete":
				p.Complete = false
				metadata, err := json.Marshal(p)
				if err != nil {
					t.Fatal(err)
				}
				put(filepath.Join(p.Dir, "point.json"), string(metadata))
			}
			if dataOnly {
				installPointBinary = func(string, string) error { t.Fatal("data-only restore attempted to install znnd"); return nil }
				err = RunRestorePointWithOptions(cfg, p.ID, RestorePointOptions{DataOnly: true})
			} else {
				err = RunRestorePoint(cfg, p.ID)
			}
			if (err == nil) != success {
				t.Fatalf("result: %v", err)
			}
			beforeStop := mode == "bad-binary" || strings.Contains(mode, "bad-archive") || strings.Contains(mode, "no-space") || strings.Contains(mode, "incomplete")
			if beforeStop && (stops != 0 || starts != 0) {
				t.Fatal("node touched before verification/space check")
			}
			if !beforeStop && (stops != 1 || starts != 1) {
				t.Fatalf("service sequence: stop=%d start=%d", stops, starts)
			}
			wantBinary, wantData := "current binary", "current chain"
			if mode == "success" || mode == "start-failure" {
				wantBinary, wantData = "point binary", "point chain"
			}
			if dataOnly && success {
				wantData = "point chain"
			}
			binary, _ := os.ReadFile(cfg.BinaryPath())
			data, _ := os.ReadFile(filepath.Join(cfg.ZnnDir, "nom", "state"))
			if string(binary) != wantBinary || string(data) != wantData {
				t.Fatal("wrong pair left on disk")
			}
			if success || mode == "start-failure" {
				if _, err := os.Stat(filepath.Join(cfg.ZnnDir, "network")); !os.IsNotExist(err) {
					t.Fatal("post-point folder retained")
				}
			}
			if mode == "success" || mode == "start-failure" {
				previous, _ := filepath.Glob(filepath.Join(cfg.InstallDir, ".nomctl-before-restore-*"))
				if len(previous) != 1 {
					t.Fatal("previous binary not retained")
				}
				saved, _ := os.ReadFile(previous[0])
				if string(saved) != "current binary" {
					t.Fatal("wrong previous binary")
				}
			}
			if dataOnly {
				installedAfter, err := os.Stat(cfg.BinaryPath())
				if err != nil || !os.SameFile(installedBefore, installedAfter) {
					t.Fatal("installed znnd replaced during data-only restore", err)
				}
				copies, err := filepath.Glob(filepath.Join(cfg.InstallDir, ".nomctl-*"))
				if err != nil || len(copies) != 0 {
					t.Fatal("data-only restore staged a binary", copies, err)
				}
			}
			for path, want := range map[string]string{filepath.Join(cfg.ZnnDir, "config.json"): "keep config", filepath.Join(cfg.ZnnDir, "wallet", "key"): "keep wallet"} {
				got, _ := os.ReadFile(path)
				if string(got) != want {
					t.Fatal("wallet/config changed")
				}
			}
			if _, err := os.Stat(p.Dir); err != nil {
				t.Fatal("restore point consumed during restoration")
			}
		})
	}
}
