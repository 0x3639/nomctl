package recovery

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestListStatusRetainsDeletedTargetsWithinSelectedScope(t *testing.T) {
	for _, point := range []bool{false, true} {
		cfg, target := fixture(t, point)
		stateDir := t.TempDir()
		report, err := Run(context.Background(), cfg, []Target{target}, Options{}, stateDir, filepath.Join(t.TempDir(), "lock"))
		if err != nil || len(report.Results) != 1 || report.Results[0].Outcome != "passed" {
			t.Fatalf("verification: %+v %v", report, err)
		}
		// Both live discovery and saved history should yield just one entry.
		targets, err := ListStatus(cfg, stateDir)
		if err != nil || len(targets) != 1 || targets[0] != target {
			t.Fatalf("duplicate or missing target: %+v %v", targets, err)
		}
		paths, err := target.paths()
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range paths {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		}
		if point {
			if err := os.Remove(target.Path); err != nil {
				t.Fatal(err)
			}
		}
		// Saved history from other services/directories must stay out of scope.
		for _, foreign := range []Target{
			{Kind: target.Kind, Path: target.Path, Service: "other-service"},
			{Kind: target.Kind, Path: filepath.Join(t.TempDir(), filepath.Base(target.Path)), Service: target.Service},
		} {
			result := report.Results[0]
			result.Target = foreign
			if err := saveResult(stateDir, result); err != nil {
				t.Fatal(err)
			}
		}
		live := Backup(cfg, cfg.ServiceName+"_backup_unverified.tar.gz")
		write(t, live.Path, []byte("not verified yet"))
		targets, err = ListStatus(cfg, stateDir)
		if err != nil || len(targets) != 2 {
			t.Fatalf("lost history or included foreign targets: %+v %v", targets, err)
		}
		seen := map[Target]bool{}
		for _, candidate := range targets {
			seen[candidate] = true
			s := SavedStatus(context.Background(), stateDir, candidate)
			if candidate == target && (s.State != "stale" || s.Last == nil || s.Last.Outcome != "passed") {
				t.Fatalf("lost successful history of deleted artifact: %+v", s)
			}
			if candidate == live && s.State != "never-verified" {
				t.Fatalf("unexpected live status: %+v", s)
			}
		}
		if !seen[target] || !seen[live] {
			t.Fatalf("missing target: %+v", targets)
		}
	}
}
