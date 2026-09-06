package backup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/0x3639/nomctl/internal/config"
)

func TestArchiveNameAndHashPath(t *testing.T) {
	ts := time.Date(2026, 9, 6, 13, 4, 5, 0, time.UTC)
	name := ArchiveName("go-zenon", ts)
	if name != "go-zenon_backup_09-06-26_130405.tar.gz" {
		t.Errorf("ArchiveName = %q", name)
	}
	if HashPath("/backup/"+name) != "/backup/go-zenon_backup_09-06-26_130405.hash" {
		t.Errorf("HashPath = %q", HashPath("/backup/"+name))
	}
}

func TestSelectForPruning(t *testing.T) {
	base := time.Now()
	var infos []Info
	for i := range 5 {
		infos = append(infos, Info{Path: string(rune('a' + i)), ModTime: base.Add(time.Duration(i) * time.Hour)})
	}
	old := SelectForPruning(infos, 3)
	if len(old) != 2 || old[0].Path != "b" || old[1].Path != "a" {
		t.Errorf("pruned = %+v", old)
	}
	if SelectForPruning(infos, 5) != nil || SelectForPruning(infos, 10) != nil {
		t.Error("nothing should be pruned when keep >= len")
	}
	if got := SelectForPruning(infos, 0); len(got) != 5 {
		t.Errorf("keep=0 should prune all, got %d", len(got))
	}
}

func TestListAndPrune(t *testing.T) {
	cfg := config.Default()
	cfg.BackupDir = t.TempDir()
	cfg.MaxBackups = 2
	base := time.Now().Add(-time.Hour)
	for i, n := range []string{"go-zenon_backup_1.tar.gz", "go-zenon_backup_2.tar.gz", "go-zenon_backup_3.tar.gz", "other_backup_9.tar.gz"} {
		p := filepath.Join(cfg.BackupDir, n)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(HashPath(p), []byte("h"), 0o644); err != nil {
			t.Fatal(err)
		}
		mt := base.Add(time.Duration(i) * time.Minute)
		if err := os.Chtimes(p, mt, mt); err != nil {
			t.Fatal(err)
		}
	}
	infos, err := List(cfg)
	if err != nil || len(infos) != 3 || infos[0].Name() != "go-zenon_backup_3" {
		t.Fatalf("List = %+v, %v", infos, err)
	}
	if err := Prune(cfg); err != nil {
		t.Fatal(err)
	}
	infos, _ = List(cfg)
	if len(infos) != 2 {
		t.Errorf("after prune: %+v", infos)
	}
	if _, err := os.Stat(filepath.Join(cfg.BackupDir, "go-zenon_backup_1.hash")); !os.IsNotExist(err) {
		t.Error("hash sidecar of pruned backup should be removed")
	}
	if _, err := os.Stat(filepath.Join(cfg.BackupDir, "other_backup_9.tar.gz")); err != nil {
		t.Error("other service's backup must be untouched")
	}
}

func TestListIgnoresIncompleteArchives(t *testing.T) {
	cfg := config.Default()
	cfg.BackupDir = t.TempDir()
	partial := filepath.Join(cfg.BackupDir, "go-zenon_backup_9.tar.gz")
	if err := os.WriteFile(partial, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	infos, err := List(cfg)
	if err != nil || len(infos) != 0 {
		t.Errorf("archive without hash must be ignored, got %+v %v", infos, err)
	}
}

func TestWriteArchive(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "f"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "go-zenon_backup_1.tar.gz")
	if err := writeArchive(src, archive); err != nil {
		t.Fatal(err)
	}
	sum, err := SHA256File(archive)
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := os.ReadFile(HashPath(archive))
	if strings.TrimSpace(string(stored)) != sum {
		t.Error("hash sidecar does not match archive")
	}
	if _, err := os.Stat(archive + ".partial"); !os.IsNotExist(err) {
		t.Error("partial file should be gone")
	}
}

func TestCadenceReached(t *testing.T) {
	now := time.Now()
	if CadenceReached(now.Add(-23*time.Hour), now, 1) {
		t.Error("23h should not satisfy a 1 day cadence")
	}
	if !CadenceReached(now.Add(-25*time.Hour), now, 1) {
		t.Error("25h should satisfy a 1 day cadence")
	}
	if !CadenceReached(now.Add(-8*24*time.Hour), now, 7) {
		t.Error("8d should satisfy 7d cadence")
	}
}

func TestScheduleTime(t *testing.T) {
	h, m := ScheduleTime("node1", "go-zenon", -1)
	if h < 2 || h > 4 || m < 0 || m > 59 {
		t.Errorf("derived time out of range: %d:%d", h, m)
	}
	h2, m2 := ScheduleTime("node1", "go-zenon", -1)
	if h != h2 || m != m2 {
		t.Error("schedule time must be deterministic")
	}
	if h3, _ := ScheduleTime("node1", "go-zenon", 23); h3 != 23 {
		t.Errorf("override ignored: %d", h3)
	}
}

func TestUnits(t *testing.T) {
	cfg := config.Default()
	cfg.MaxBackups = 5
	cfg.BackupCadenceDays = 7
	svc := ServiceUnit(cfg, "/usr/local/bin/nomctl")
	if !strings.Contains(svc, "ExecStart=/usr/local/bin/nomctl backup --skip-preflight --max-backups 5 --cadence 7") {
		t.Errorf("service unit:\n%s", svc)
	}
	if !strings.Contains(svc, `Environment="NOMCTL_BACKUP_DIR=/backup"`) || !strings.Contains(svc, `Environment="NOMCTL_MIN_FREE_SPACE_KB=15728640"`) {
		t.Errorf("service unit should carry backup dir and min free space:\n%s", svc)
	}
	cfg.BackupDir = `/mnt/my "backups"`
	svc = ServiceUnit(cfg, "/usr/local/bin/nomctl")
	if !strings.Contains(svc, `Environment="NOMCTL_BACKUP_DIR=/mnt/my \"backups\""`) {
		t.Errorf("quotes should be escaped:\n%s", svc)
	}
	timer := TimerUnit(3, 7)
	if !strings.Contains(timer, "OnCalendar=*-*-* 03:07:00") || !strings.Contains(timer, "Persistent=true") {
		t.Errorf("timer unit:\n%s", timer)
	}
}

func TestParseCadence(t *testing.T) {
	cfg := config.Default()
	cfg.BackupCadenceDays = 7
	if n, ok := ParseCadence(ServiceUnit(cfg, "/usr/local/bin/nomctl")); !ok || n != 7 {
		t.Errorf("cadence = %d %v", n, ok)
	}
	if _, ok := ParseCadence("[Service]\nExecStart=/bin/true\n"); ok {
		t.Error("no cadence flag")
	}
}

func TestSHA256File(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(p, []byte("abc"), 0o644); err != nil {
		t.Fatal(err)
	}
	sum, err := SHA256File(p)
	if err != nil || sum != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Errorf("sum = %s, %v", sum, err)
	}
}
