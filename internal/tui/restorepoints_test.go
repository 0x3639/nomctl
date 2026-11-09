package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/0x3639/nomctl/internal/backup"
	"github.com/0x3639/nomctl/internal/config"
)

func TestRestorePointDeletionRevalidatesEntirePreview(t *testing.T) {
	for _, mode := range []string{"unchanged", "completed", "retention-changed", "missing", "not-eligible", "indefinite-individual", "damaged-individual"} {
		t.Run(mode, func(t *testing.T) {
			cfg := config.Default()
			cfg.BackupDir = t.TempDir()
			now := time.Now().UTC()
			put := func(p backup.RestorePoint) {
				t.Helper()
				dir := filepath.Join(backup.RestorePointsDir(cfg), p.ID)
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				data, err := json.Marshal(p)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "point.json"), data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			first := backup.RestorePoint{ID: cfg.ServiceName + "_point_first", Service: cfg.ServiceName, CreatedAt: now.Add(-48 * time.Hour), KeepDays: 1, Complete: true}
			second := first
			second.ID = cfg.ServiceName + "_point_second"
			if mode == "completed" {
				second.Complete = false
			}
			if mode == "not-eligible" || mode == "indefinite-individual" {
				second.KeepDays = 0
			}
			put(first)
			put(second)
			if mode == "damaged-individual" {
				if err := os.WriteFile(filepath.Join(backup.RestorePointsDir(cfg), second.ID, "point.json"), []byte("broken"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			preview, err := backup.ListRestorePoints(cfg)
			if err != nil || len(preview) != 2 {
				t.Fatalf("preview: %v %v", preview, err)
			}
			switch mode {
			case "completed":
				second.Complete = true
				put(second)
			case "retention-changed":
				second.KeepDays = 0
				put(second)
			case "missing":
				if err := os.RemoveAll(filepath.Join(backup.RestorePointsDir(cfg), second.ID)); err != nil {
					t.Fatal(err)
				}
			}
			prune := mode != "completed" && mode != "indefinite-individual" && mode != "damaged-individual"
			err = deleteSelectedRestorePoints(cfg, preview, prune, now)
			wantDelete := mode == "unchanged" || mode == "indefinite-individual" || mode == "damaged-individual"
			if (err == nil) != wantDelete {
				t.Fatalf("deletion result: %v", err)
			}
			_, statErr := os.Stat(filepath.Join(backup.RestorePointsDir(cfg), first.ID))
			if os.IsNotExist(statErr) != wantDelete {
				t.Fatalf("unexpected deletion of first point: %v", statErr)
			}
		})
	}
}
