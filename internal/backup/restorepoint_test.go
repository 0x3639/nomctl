package backup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/0x3639/nomctl/internal/config"
)

func pointFixture(t *testing.T, days int) (config.Config, RestorePoint) {
	t.Helper()
	mockCapacity(t, func(string) (int64, int64, error) { return 1 << 40, 0, nil })
	cfg := testConfig(t)
	cfg.InstallDir = t.TempDir()
	cfg.RestorePointKeepDays = days
	if err := os.WriteFile(cfg.BinaryPath(), []byte("original binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(cfg.ZnnDir, "nom"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.ZnnDir, "nom", "state"), []byte("original chain"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := PrepareRestorePoint(cfg, "Before test upgrade")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Capture(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Finish(); err != nil {
		t.Fatal(err)
	}
	points, err := ListRestorePoints(cfg)
	if err != nil || len(points) != 1 {
		t.Fatalf("points: %+v %v", points, err)
	}
	return cfg, points[0]
}

func TestRestorePointsSurviveAgeCountAndDiskPressure(t *testing.T) {
	for _, days := range []int{0, 7, 30} {
		t.Run(strings.Repeat("d", days+1), func(t *testing.T) {
			cfg, p := pointFixture(t, days)
			p.CreatedAt = time.Now().Add(-60 * 24 * time.Hour)
			if err := p.save(); err != nil {
				t.Fatal(err)
			}
			if p.Eligible(time.Now()) != (days > 0) {
				t.Fatal("incorrect pruning eligibility")
			}
			if err := VerifyRestorePointBinary(p); err != nil {
				t.Fatal(err)
			}
			cfg.MaxBackups = 1
			cfg.BackupRetentionDays = 1
			archiveFixture(t, cfg, "expired", 48*time.Hour)
			if err := Prune(cfg); err != nil {
				t.Fatal(err)
			}
			mockCapacity(t, func(string) (int64, int64, error) { return 20 << 20, 180 << 20, nil })
			if err := ensureSpace(cfg, 1); err == nil {
				t.Fatal("disk pressure ignored")
			}
			for _, path := range []string{p.ArchivePath(), HashPath(p.ArchivePath()), p.BinaryPath(), filepath.Join(p.Dir, "point.json")} {
				if _, err := os.Stat(path); err != nil {
					t.Fatalf("protected point deleted: %v", err)
				}
			}
			points, err := ListRestorePoints(cfg)
			if err != nil || len(points) != 1 || points[0].Size == 0 {
				t.Fatalf("protected point missing: %+v %v", points, err)
			}
			if err := DeleteBackup(cfg, p.ArchivePath()); err == nil {
				t.Fatal("ordinary backup deletion accepted a protected point")
			}
			if err := DeleteRestorePoint(cfg, p.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(p.Dir); !os.IsNotExist(err) {
				t.Fatal("explicit deletion did not remove point")
			}
		})
	}
}

func TestRestorePointIncompleteAndPathSafety(t *testing.T) {
	cfg, p := pointFixture(t, 0)
	if err := os.WriteFile(filepath.Join(p.Dir, "point.json"), []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	points, err := ListRestorePoints(cfg)
	if err != nil || len(points) != 1 || points[0].Problem == "" || points[0].Eligible(time.Now()) {
		t.Fatalf("damaged point not exposed for manual cleanup: %+v %v", points, err)
	}
	for _, id := range []string{"", "..", "../outside", cfg.ServiceName + "_point_../../outside", "other_point_test"} {
		if err := DeleteRestorePoint(cfg, id); err == nil {
			t.Fatalf("unsafe ID accepted: %q", id)
		}
	}
	external := t.TempDir()
	link := cfg.ServiceName + "_point_link"
	if err := os.Symlink(external, filepath.Join(RestorePointsDir(cfg), link)); err != nil {
		t.Fatal(err)
	}
	if err := DeleteRestorePoint(cfg, link); err == nil {
		t.Fatal("symlink deletion accepted")
	}
	if err := DeleteRestorePoint(cfg, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(external); err != nil {
		t.Fatal("external directory changed", err)
	}
}

func TestPointSpaceCheckIncludesBinaryAndLeavesNoPoint(t *testing.T) {
	cfg := testConfig(t)
	cfg.InstallDir = t.TempDir()
	f, err := os.Create(cfg.BinaryPath())
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(40 << 20); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	mockCapacity(t, func(string) (int64, int64, error) { return 100 << 20, 100 << 20, nil })
	if _, err := PrepareRestorePoint(cfg, "not enough room"); err == nil {
		t.Fatal("binary size ignored")
	}
	points, err := ListRestorePoints(cfg)
	if err != nil || len(points) != 0 {
		t.Fatal("point created despite insufficient space", err)
	}
}

func TestRestorePointKeepParsing(t *testing.T) {
	if got, err := ParseRestorePointKeep(" Indefinite "); err != nil || got != 0 {
		t.Fatal("whitespace handling failed", err)
	}
	for input, want := range map[string]int{"indefinite": 0, "1": 1, "7": 7, "30": 30} {
		got, err := ParseRestorePointKeep(input)
		if err != nil || got != want {
			t.Fatalf("%q: %d %v", input, got, err)
		}
	}
	for _, input := range []string{"", "0", "31", "-1", "week"} {
		if _, err := ParseRestorePointKeep(input); err == nil {
			t.Fatalf("invalid retention accepted: %q", input)
		}
	}
}
