package orchestrator

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/0x3639/nomctl/internal/backup"
	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/service"
)

func populateOrchestratorDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"events/eth/000001.ldb":        "eth-events",
		"events/bnb/000001.ldb":        "bnb-events",
		"queues/eth/pending.dat":       "eth-queue",
		"config.json":                  `{"DataPath":"` + dir + `","ProducerKeyFileName":"producer","TssConfig":{"BaseDir":"` + filepath.Join(dir, "tss") + `"}}`,
		"producer":                     "encrypted-producer-key",
		"tss/localstate-1.json":        "tss-secret-share",
		"unwrap-pool-votes/000001.ldb": "votes",
	}
	for rel, body := range files {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func listTarEntries(t *testing.T, archive string) []string {
	t.Helper()
	f, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	var entries []string
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, hdr.Name)
	}
	return entries
}

func TestBackupSyncDataOnlyIncludesEventsAndQueues(t *testing.T) {
	dir := populateOrchestratorDir(t)
	outDir := filepath.Join(t.TempDir(), "backups")
	calls := stubDeployHost(t, true, service.Active, filepath.Join(t.TempDir(), "orchestrator.service"))

	cfg := config.Default()
	cfg.OrchestratorDir = dir

	res, err := BackupSyncData(context.Background(), cfg, SyncBackupOptions{
		OutputDir: outDir,
		Now:       func() time.Time { return time.Date(2026, 11, 26, 20, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(*calls, ",") != "stop orchestrator,start orchestrator" {
		t.Fatalf("calls = %v", *calls)
	}
	gotSHA, err := backup.SHA256File(res.ArchivePath)
	if err != nil || gotSHA != res.SHA256 {
		t.Fatalf("archive sha mismatch: got %q, want %q (%v)", gotSHA, res.SHA256, err)
	}
	hashBytes, err := os.ReadFile(res.HashPath)
	if err != nil || strings.TrimSpace(string(hashBytes)) != res.SHA256 {
		t.Fatalf("hash sidecar mismatch: %q (%v)", string(hashBytes), err)
	}

	entries := listTarEntries(t, res.ArchivePath)
	hasEvents, hasQueues := false, false
	for _, e := range entries {
		top := strings.Split(e, "/")[0]
		if !IsSyncDir(top) {
			t.Fatalf("sync backup leaked non-sync entry: %s", e)
		}
		if e == "events/eth/000001.ldb" {
			hasEvents = true
		}
		if e == "queues/eth/pending.dat" {
			hasQueues = true
		}
	}
	if !hasEvents || !hasQueues {
		t.Fatalf("expected events and queues files in archive, got %v", entries)
	}
}

func TestBackupSyncDataRefusesKeyOverlapAndSymlinks(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "events"), 0o700); err != nil {
		t.Fatal(err)
	}
	badCfg := `{"TssConfig":{"BaseDir":"` + filepath.Join(dir, "events", "tss") + `"}}`
	if err := os.WriteFile(ConfigPath(dir), []byte(badCfg), 0o600); err != nil {
		t.Fatal(err)
	}
	calls := stubDeployHost(t, true, service.Active, filepath.Join(t.TempDir(), "orchestrator.service"))
	cfg := config.Default()
	cfg.OrchestratorDir = dir
	if _, err := BackupSyncData(context.Background(), cfg, SyncBackupOptions{OutputDir: t.TempDir()}); err == nil {
		t.Fatal("expected overlap error")
	}
	if len(*calls) != 0 {
		t.Fatalf("service must not stop on key overlap: %v", *calls)
	}

	// Symlink inside events must be rejected and service restarted.
	dir2 := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir2, "events"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(dir2, "events", "badlink")); err != nil {
		t.Fatal(err)
	}
	cfg.OrchestratorDir = dir2
	if _, err := BackupSyncData(context.Background(), cfg, SyncBackupOptions{OutputDir: t.TempDir()}); err == nil {
		t.Fatal("expected symlink rejection")
	}
	if strings.Join(*calls, ",") != "stop orchestrator,start orchestrator" {
		t.Fatalf("orchestrator must restart after failed copy: %v", *calls)
	}
}

func TestCopySyncDataSequenceAndFailureRecovery(t *testing.T) {
	dir := populateOrchestratorDir(t)
	calls := stubDeployHost(t, true, service.Active, filepath.Join(t.TempDir(), "orchestrator.service"))

	oldSSH, oldRsync, oldTool := sshRun, rsyncRun, toolExists
	t.Cleanup(func() { sshRun, rsyncRun, toolExists = oldSSH, oldRsync, oldTool })
	toolExists = func(string) bool { return true }

	var sshScripts []string
	var syncedSources []string
	var syncedStage string
	sshRun = func(_ context.Context, _ SyncCopyOptions, script string) (string, error) {
		sshScripts = append(sshScripts, script)
		if len(sshScripts) == 1 {
			return "LOAD=loaded\nACTIVE=active\n", nil
		}
		return "", nil
	}
	rsyncRun = func(_ context.Context, _ SyncCopyOptions, sources []string, remoteStage string) error {
		syncedSources = append([]string{}, sources...)
		syncedStage = remoteStage
		return nil
	}

	cfg := config.Default()
	cfg.OrchestratorDir = dir
	res, err := CopySyncData(context.Background(), cfg, SyncCopyOptions{
		Host: "root@target.example",
		Port: 2222,
		Now:  func() time.Time { return time.Date(2026, 11, 26, 20, 30, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.LocalRestarted || !res.RemoteRestarted || !res.RemoteUnitFound {
		t.Fatalf("unexpected result: %+v", res)
	}
	if strings.Join(res.CopiedDirs, ",") != "events,queues" {
		t.Fatalf("CopiedDirs = %v", res.CopiedDirs)
	}
	// Verify ONLY events and queues were handed to rsync.
	if len(syncedSources) != 2 || filepath.Base(syncedSources[0]) != "events" || filepath.Base(syncedSources[1]) != "queues" {
		t.Fatalf("rsync sources leaked extra files: %v", syncedSources)
	}
	if !strings.Contains(syncedStage, ".sync-copy-stage-20261126-203000") {
		t.Fatalf("unexpected remote stage: %q", syncedStage)
	}
	// Cutover script must stop remote, keep safety backup, swap, and restart remote.
	cutover := sshScripts[len(sshScripts)-1]
	for _, want := range []string{"systemctl stop 'orchestrator'", "sync-backup-before-copy-20261126-203000", "rollback()", "systemctl start 'orchestrator'"} {
		if !strings.Contains(cutover, want) {
			t.Errorf("cutover script missing %q:\n%s", want, cutover)
		}
	}
	if strings.Join(*calls, ",") != "stop orchestrator,start orchestrator" {
		t.Fatalf("local calls = %v", *calls)
	}

	// Failure during rsync must clean up remote staging and restart local orchestrator.
	*calls = nil
	sshScripts = nil
	rsyncRun = func(_ context.Context, _ SyncCopyOptions, _ []string, _ string) error {
		return errors.New("network dropped")
	}
	if _, err := CopySyncData(context.Background(), cfg, SyncCopyOptions{
		Host: "root@target.example",
	}); err == nil {
		t.Fatal("expected rsync error")
	}
	if strings.Join(*calls, ",") != "stop orchestrator,start orchestrator" {
		t.Fatalf("local orchestrator must restart after rsync failure: %v", *calls)
	}
	if len(sshScripts) != 3 || !strings.Contains(sshScripts[2], "rm -rf") {
		t.Fatalf("expected staging cleanup script on rsync failure, got %v", sshScripts)
	}
}

func TestSyncCopyOptionsValidation(t *testing.T) {
	cfg := config.Default()
	for _, bad := range []SyncCopyOptions{
		{Host: ""},
		{Host: "-oProxyCommand=evil"},
		{Host: "root@host;rm -rf /"},
		{Host: "root@host", Port: 70000},
		{Host: "root@host", RemoteDir: "/"},
		{Host: "root@host", RemoteDir: "relative"},
		{Host: "root@host", RemoteService: "bad service;name"},
	} {
		if err := bad.Validate(cfg); err == nil {
			t.Errorf("expected validation error for %+v", bad)
		}
	}
}
