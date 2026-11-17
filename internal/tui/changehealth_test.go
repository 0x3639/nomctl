package tui

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/0x3639/nomctl/internal/backup"
	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/health"
	"github.com/0x3639/nomctl/internal/lock"
	"github.com/0x3639/nomctl/internal/restore"
)

func TestRecoverChangeOnlyOffersCompletePointsAndPreservesErrors(t *testing.T) {
	for _, mode := range []string{"missing", "invalid", "incomplete", "declined", "cancelled", "busy", "recovery-failed", "recovered"} {
		t.Run(mode, func(t *testing.T) {
			cfg := config.Default()
			cfg.BackupDir = t.TempDir()
			id := cfg.ServiceName + "_point_recovery"
			if mode != "missing" {
				point := backup.RestorePoint{ID: id, Service: cfg.ServiceName, CreatedAt: time.Now().UTC(), Complete: mode != "incomplete"}
				data, err := json.Marshal(point)
				if err != nil {
					t.Fatal(err)
				}
				if mode == "invalid" {
					data = []byte("invalid metadata")
				}
				dir := filepath.Join(backup.RestorePointsDir(cfg), id)
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "point.json"), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			cause := errors.New("upgrade failed; snapshot retained")
			original := &health.Failure{RecoveryID: id, DataOnly: true, Cause: cause}
			recoveryErr := errors.New("restore failed")
			if mode == "busy" {
				recoveryErr = lock.ErrBusy
			}
			prompts, attempts := 0, 0
			got := recoverChange(cfg, original, func(prompt string) (bool, error) {
				prompts++
				if !strings.Contains(prompt, id) || !strings.Contains(prompt, "keep installed znnd") {
					t.Fatalf("wrong recovery offer: %s", prompt)
				}
				if mode == "cancelled" {
					return false, ErrCancelled
				}
				return mode != "declined", nil
			}, func(recoveryID string, opts restore.RestorePointOptions) error {
				attempts++
				if recoveryID != id || !opts.DataOnly {
					t.Fatal("changed recovery selection")
				}
				if mode == "recovered" {
					return nil
				}
				return recoveryErr
			})
			switch mode {
			case "missing", "invalid", "incomplete":
				if prompts != 0 || attempts != 0 || !errors.Is(got, original) {
					t.Fatalf("offered unusable point: %d %d %v", prompts, attempts, got)
				}
			case "declined", "cancelled":
				if prompts != 1 || attempts != 0 || !errors.Is(got, cause) {
					t.Fatalf("unexpected recovery: %d %d %v", prompts, attempts, got)
				}
				if mode == "cancelled" && !errors.Is(got, ErrCancelled) {
					t.Fatal("lost cancellation", got)
				}
			case "busy", "recovery-failed":
				var failure *health.Failure
				if prompts != 1 || attempts != 1 || !errors.Is(got, cause) || !errors.Is(got, recoveryErr) || !errors.As(got, &failure) || failure != original {
					t.Fatalf("lost original or recovery error: %d %d %v", prompts, attempts, got)
				}
			case "recovered":
				if prompts != 1 || attempts != 1 || got != nil {
					t.Fatalf("successful recovery: %d %d %v", prompts, attempts, got)
				}
			}
		})
	}
}
