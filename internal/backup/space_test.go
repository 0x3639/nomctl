package backup

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/0x3639/nomctl/internal/config"
)

func testConfig(t *testing.T) config.Config {
	t.Helper()
	cfg := config.Default()
	cfg.BackupDir = t.TempDir()
	cfg.ZnnDir = t.TempDir()
	cfg.MinFreeSpaceKB = 0
	return cfg
}

func archiveFixture(t *testing.T, cfg config.Config, name string, age time.Duration) string {
	t.Helper()
	p := filepath.Join(cfg.BackupDir, cfg.ServiceName+"_backup_"+name+".tar.gz")
	for _, path := range []string{p, HashPath(p)} {
		if err := os.WriteFile(path, []byte("archive"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mt := time.Now().Add(-age)
	if err := os.Chtimes(p, mt, mt); err != nil {
		t.Fatal(err)
	}
	return p
}

func mockCapacity(t *testing.T, fn func(string) (int64, int64, error)) {
	t.Helper()
	previous := diskCapacity
	diskCapacity = fn
	t.Cleanup(func() { diskCapacity = previous })
}

func TestRetentionDays(t *testing.T) {
	for _, days := range []int{1, 7, 30} {
		t.Run(string(rune('a'+days)), func(t *testing.T) {
			cfg := testConfig(t)
			cfg.BackupRetentionDays, cfg.MaxBackups = days, 30
			old := archiveFixture(t, cfg, "expired", time.Duration(days)*24*time.Hour+time.Minute)
			newer := archiveFixture(t, cfg, "recent", time.Hour)
			wallet := filepath.Join(cfg.BackupDir, "wallet")
			if err := os.Mkdir(wallet, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(wallet, "secret.age"), []byte("private"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := Prune(cfg); err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{old, HashPath(old)} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("expired backup retained: %s", path)
				}
			}
			for _, path := range []string{newer, filepath.Join(wallet, "secret.age")} {
				if _, err := os.Stat(path); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestSpacePrunesOldestEvenWithinRetention(t *testing.T) {
	cfg := testConfig(t)
	old := archiveFixture(t, cfg, "old", 2*time.Hour)
	newer := archiveFixture(t, cfg, "new", time.Hour)
	var deletedOrder []string
	previousCount := 2
	mockCapacity(t, func(string) (int64, int64, error) {
		infos, err := List(cfg)
		if err != nil {
			return 0, 0, err
		}
		if len(infos) < previousCount {
			if len(infos) == 1 {
				if infos[0].Path != newer {
					t.Fatal("newest pruned before oldest")
				}
				deletedOrder = append(deletedOrder, old)
			} else {
				deletedOrder = append(deletedOrder, newer)
			}
		}
		previousCount = len(infos)
		used := int64(100+len(infos)*20) << 20
		return 200<<20 - used, used, nil
	})
	if err := ensureSpace(cfg, 30<<20); err != nil {
		t.Fatal(err)
	}
	if len(deletedOrder) != 2 {
		t.Fatalf("expected both archives pruned, got %v", deletedOrder)
	}
}

func TestSpaceLimitsAndErrors(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		used, additional, minKB int64
		fail                    bool
	}{
		{"exact limit including margin", 100, 34, 0, false},
		{"above limit", 100, 35, 0, true},
		{"already above limit", 160, 0, 0, true},
		{"minimum free space", 50, 1, 150 * 1024, true},
		{"large minimum avoids overflow", 50, 1, 1 << 62, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig(t)
			cfg.MinFreeSpaceKB = tc.minKB
			mockCapacity(t, func(string) (int64, int64, error) { return (200 - tc.used) << 20, tc.used << 20, nil })
			if err := ensureSpace(cfg, tc.additional<<20); (err != nil) != tc.fail {
				t.Fatalf("unexpected space result: %v", err)
			}
		})
	}
	t.Run("stat failure", func(t *testing.T) {
		cfg := testConfig(t)
		mockCapacity(t, func(string) (int64, int64, error) { return 0, 0, errors.New("stat failed") })
		if err := ensureSpace(cfg, 0); err == nil {
			t.Fatal("disk check error ignored")
		}
	})
}

func TestWriterStopsWhenOtherFilesGrow(t *testing.T) {
	cfg := testConfig(t)
	used := int64(100 << 20)
	mockCapacity(t, func(string) (int64, int64, error) { return 200<<20 - used, used, nil })
	var dst bytes.Buffer
	w := spaceWriter{cfg: cfg, dst: &dst}
	if _, err := w.Write([]byte("first")); err != nil {
		t.Fatal(err)
	}
	used = 150 << 20
	if _, err := w.Write([]byte("second")); err == nil {
		t.Fatal("unsafe write accepted")
	}
	if dst.String() != "first" {
		t.Fatal("writer exceeded disk allowance")
	}
}

func TestPrepareReservesCopyAndArchiveBeforeServiceStop(t *testing.T) {
	cfg := testConfig(t)
	if err := os.Mkdir(filepath.Join(cfg.ZnnDir, "nom"), 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(cfg.ZnnDir, "nom", "sparse"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(20 << 20); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	mockCapacity(t, func(string) (int64, int64, error) { return 100 << 20, 100 << 20, nil })
	if _, err := Prepare(cfg); err == nil || !strings.Contains(err.Error(), "75%") {
		t.Fatalf("peak staging size ignored: %v", err)
	}
	entries, err := os.ReadDir(cfg.BackupDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("staging created without space: %v %v", entries, err)
	}
}

func TestCopyBudgetRejectsLinks(t *testing.T) {
	cfg := testConfig(t)
	if err := os.Symlink(t.TempDir(), filepath.Join(cfg.ZnnDir, "nom")); err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(cfg); err == nil {
		t.Fatal("symlink source accepted")
	}
}

func TestArchiveWritePressureRemovesPartialAndReportsCause(t *testing.T) {
	cfg := testConfig(t)
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "chain"), []byte("chain data"), 0o600); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(cfg.BackupDir, "go-zenon_backup_pressure.tar.gz")
	mockCapacity(t, func(string) (int64, int64, error) {
		if _, err := os.Stat(archive + ".partial"); err == nil {
			return 50 << 20, 150 << 20, nil
		}
		return 150 << 20, 50 << 20, nil
	})
	if err := writeArchiveGuarded(src, archive, &cfg); err == nil || !strings.Contains(err.Error(), "75%") {
		t.Fatalf("disk-pressure failure lost: %v", err)
	}
	for _, path := range []string{archive, archive + ".partial", HashPath(archive)} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("failed archive left behind: %s", path)
		}
	}
	if _, err := os.Stat(filepath.Join(src, "chain")); err != nil {
		t.Fatal("recovery data lost", err)
	}
}
