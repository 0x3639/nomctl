package orchestrator

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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
			return "CONFIG_PRESENT=0\nLOAD=loaded\nACTIVE=active\n", nil
		}
		if strings.Contains(script, "REMOTE_RESTARTED=") {
			return "REMOTE_UNIT_FOUND=1\nREMOTE_RESTARTED=1\n", nil
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
	fixedNow := func() time.Time { return time.Date(2026, 11, 26, 20, 30, 0, 0, time.UTC) }
	res, err := CopySyncData(context.Background(), cfg, SyncCopyOptions{
		Host: "root@target.example",
		Port: 2222,
		Now:  fixedNow,
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
	if !strings.Contains(syncedStage, ".sync-copy-stage-20261126-203000-") {
		t.Fatalf("unexpected remote stage: %q", syncedStage)
	}
	// Stage creation must not rm -rf any existing directory.
	if strings.Contains(sshScripts[1], "rm -rf") {
		t.Fatalf("stage create script must not rm -rf existing stage:\n%s", sshScripts[1])
	}
	// Cutover script must acquire destination lock, create backup dir BEFORE stop,
	// inspect live unit existence/ActiveState, record installed_dirs before mv,
	// keep rollback armed across start, and emit REMOTE_UNIT_FOUND + REMOTE_RESTARTED.
	cutover := sshScripts[len(sshScripts)-1]
	for _, want := range []string{
		".nomctl-sync-cutover.lock",
		"mkdir -m 0700 " + sshQuote(res.RemoteBackup),
		"trap on_exit EXIT",
		"trap 'exit 143' TERM",
		"load_state=$(systemctl show -p LoadState --value 'orchestrator')",
		"active_before=$(systemctl show -p ActiveState --value 'orchestrator')",
		"stop_remote_unit",
		"rollback()",
		"systemctl start 'orchestrator'",
		"REMOTE_UNIT_FOUND=$unit_found",
		"REMOTE_RESTARTED=$restart_remote",
	} {
		if !strings.Contains(cutover, want) {
			t.Errorf("cutover script missing %q:\n%s", want, cutover)
		}
	}
	if strings.Contains(cutover, "|| echo inactive") {
		t.Fatalf("cutover script must not swallow ActiveState errors:\n%s", cutover)
	}
	if strings.Index(cutover, "mkdir -m 0700") > strings.Index(cutover, "stop_remote_unit\nfi") {
		t.Fatalf("backup mkdir must precede stop_remote_unit:\n%s", cutover)
	}
	if strings.Index(cutover, `installed_dirs="$installed_dirs $d"`) > strings.Index(cutover, `mv `+sshQuote(syncedStage)+`/"$d"`) {
		t.Fatalf("installed_dirs must be recorded before stage-to-live mv:\n%s", cutover)
	}
	if strings.Index(cutover, "  swapped=0\n") > strings.Index(cutover, "  need_restart=0\n") {
		t.Fatalf("swapped=0 must be cleared before need_restart=0:\n%s", cutover)
	}
	if out, err := exec.Command("sh", "-n", "-c", cutover).CombinedOutput(); err != nil {
		t.Fatalf("cutover script failed sh -n syntax check: %v\n%s", err, string(out))
	}
	if strings.Join(*calls, ",") != "stop orchestrator,start orchestrator" {
		t.Fatalf("local calls = %v", *calls)
	}

	// Second copy at the exact same UTC second must get a distinct stage and backup dir.
	firstStage := syncedStage
	firstBackup := res.RemoteBackup
	sshScripts = nil
	resSameSecond, err := CopySyncData(context.Background(), cfg, SyncCopyOptions{
		Host: "root@target.example",
		Port: 2222,
		Now:  fixedNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	if syncedStage == firstStage || resSameSecond.RemoteBackup == firstBackup {
		t.Fatalf("same-second copies collided: stage %q vs %q, backup %q vs %q", syncedStage, firstStage, resSameSecond.RemoteBackup, firstBackup)
	}

	// If the remote service stopped between preflight and cutover, RemoteRestarted
	// must reflect the cutover sample (false), not the stale preflight probe.
	*calls = nil
	sshScripts = nil
	sshRun = func(_ context.Context, _ SyncCopyOptions, script string) (string, error) {
		sshScripts = append(sshScripts, script)
		if len(sshScripts) == 1 {
			return "CONFIG_PRESENT=0\nLOAD=loaded\nACTIVE=active\n", nil
		}
		if strings.Contains(script, "REMOTE_RESTARTED=") {
			return "REMOTE_UNIT_FOUND=1\nREMOTE_RESTARTED=0\n", nil
		}
		return "", nil
	}
	resStopped, err := CopySyncData(context.Background(), cfg, SyncCopyOptions{
		Host: "root@target.example",
		Now:  func() time.Time { return time.Date(2026, 11, 26, 20, 31, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if !resStopped.RemoteUnitFound || resStopped.RemoteRestarted {
		t.Fatalf("expected RemoteUnitFound=true RemoteRestarted=false when cutover observed inactive remote unit: %+v", resStopped)
	}

	// If unit was installed between preflight and cutover, result reflects cutover's REMOTE_UNIT_FOUND=1.
	*calls = nil
	sshScripts = nil
	sshRun = func(_ context.Context, _ SyncCopyOptions, script string) (string, error) {
		sshScripts = append(sshScripts, script)
		if len(sshScripts) == 1 {
			return "CONFIG_PRESENT=0\nLOAD=not-found\nACTIVE=inactive\n", nil
		}
		if strings.Contains(script, "REMOTE_RESTARTED=") {
			return "REMOTE_UNIT_FOUND=1\nREMOTE_RESTARTED=1\n", nil
		}
		return "", nil
	}
	resInstalledLate, err := CopySyncData(context.Background(), cfg, SyncCopyOptions{
		Host: "root@target.example",
		Now:  func() time.Time { return time.Date(2026, 11, 26, 20, 31, 30, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if !resInstalledLate.RemoteUnitFound || !resInstalledLate.RemoteRestarted {
		t.Fatalf("expected RemoteUnitFound=true RemoteRestarted=true from cutover output: %+v", resInstalledLate)
	}

	// Missing REMOTE_RESTARTED/REMOTE_UNIT_FOUND status line must fail rather than report success.
	*calls = nil
	sshScripts = nil
	sshRun = func(_ context.Context, _ SyncCopyOptions, script string) (string, error) {
		sshScripts = append(sshScripts, script)
		if len(sshScripts) == 1 {
			return "CONFIG_PRESENT=0\nLOAD=loaded\nACTIVE=active\n", nil
		}
		return "", nil
	}
	if _, err := CopySyncData(context.Background(), cfg, SyncCopyOptions{
		Host: "root@target.example",
		Now:  func() time.Time { return time.Date(2026, 11, 26, 20, 32, 0, 0, time.UTC) },
	}); err == nil || !strings.Contains(err.Error(), "REMOTE_RESTARTED") {
		t.Fatalf("expected missing REMOTE_RESTARTED error, got %v", err)
	}
	if strings.Join(*calls, ",") != "stop orchestrator,start orchestrator" {
		t.Fatalf("local orchestrator must restart when cutover output is incomplete: %v", *calls)
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

func TestCopySyncDataValidatesDestinationLayoutBeforeStoppingLocal(t *testing.T) {
	probePayload := func(cfgJSON string) string {
		sum := sha256.Sum256([]byte(cfgJSON))
		return fmt.Sprintf("CONFIG_PRESENT=1\nCONFIG_SHA256=%s\nCONFIG_B64_BEGIN\n%s\nCONFIG_B64_END\nLOAD=loaded\nACTIVE=active\n",
			hex.EncodeToString(sum[:]), base64.StdEncoding.EncodeToString([]byte(cfgJSON)))
	}

	// 1. Destination passphrase file under events/ must be rejected before stopping local service.
	dir := populateOrchestratorDir(t)
	calls := stubDeployHost(t, true, service.Active, filepath.Join(t.TempDir(), "orchestrator.service"))
	oldSSH, oldRsync, oldTool := sshRun, rsyncRun, toolExists
	t.Cleanup(func() { sshRun, rsyncRun, toolExists = oldSSH, oldRsync, oldTool })
	toolExists = func(string) bool { return true }

	remoteDir := "/srv/remote/.orchestrator"
	sshRun = func(_ context.Context, _ SyncCopyOptions, _ string) (string, error) {
		return probePayload(`{"ProducerKeyFilePassphraseFile":"events/pass.txt"}`), nil
	}
	rsyncCalled := false
	rsyncRun = func(_ context.Context, _ SyncCopyOptions, _ []string, _ string) error {
		rsyncCalled = true
		return nil
	}

	cfg := config.Default()
	cfg.OrchestratorDir = dir
	if _, err := CopySyncData(context.Background(), cfg, SyncCopyOptions{
		Host:      "root@target.example",
		RemoteDir: remoteDir,
	}); err == nil || !strings.Contains(err.Error(), "overlaps sync directory") {
		t.Fatalf("expected destination overlap rejection, got %v", err)
	}
	if len(*calls) != 0 || rsyncCalled {
		t.Fatalf("local service/rsync must not run when destination layout overlaps secrets: calls=%v rsync=%v", *calls, rsyncCalled)
	}

	// 2. Destination protected path inside queues/ is allowed when source only replaces events/.
	eventsOnlyDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(eventsOnlyDir, "events"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(eventsOnlyDir, "events", "0001.ldb"), []byte("ev"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.OrchestratorDir = eventsOnlyDir
	var lastCutover string
	sshCalls := 0
	sshRun = func(_ context.Context, _ SyncCopyOptions, script string) (string, error) {
		sshCalls++
		if sshCalls == 1 {
			return probePayload(`{"ProducerKeyFileName":"producer","ProducerKeyFilePassphraseFile":"queues/pass.txt","TssConfig":{"BaseDir":"/srv/remote/.orchestrator/tss"}}`), nil
		}
		if strings.Contains(script, "REMOTE_RESTARTED=") {
			lastCutover = script
			return "REMOTE_UNIT_FOUND=1\nREMOTE_RESTARTED=1\n", nil
		}
		return "", nil
	}
	if _, err := CopySyncData(context.Background(), cfg, SyncCopyOptions{
		Host:      "root@target.example",
		RemoteDir: remoteDir,
	}); err != nil {
		t.Fatalf("copy should allow protected path outside replaced directories, got %v", err)
	}
	if !strings.Contains(lastCutover, "remote orchestrator config changed during sync copy") {
		t.Fatalf("cutover script must verify remote config SHA-256 stayed unchanged:\n%s", lastCutover)
	}

	// 3. Real POSIX preflight script execution round-trips through parseRemoteProbe + checkSyncLayoutBytes.
	tmpPreflight := t.TempDir()
	realRemote := filepath.Join(tmpPreflight, ".orchestrator")
	if err := os.MkdirAll(realRemote, 0o700); err != nil {
		t.Fatal(err)
	}
	badCfg := `{"ProducerKeyFilePassphraseFile":"` + filepath.Join(realRemote, "events", "pass") + `"}`
	if err := os.WriteFile(ConfigPath(realRemote), []byte(badCfg), 0o600); err != nil {
		t.Fatal(err)
	}
	preflightBin := filepath.Join(tmpPreflight, "bin")
	if err := os.MkdirAll(preflightBin, 0o755); err != nil {
		t.Fatal(err)
	}
	writeDefaultSystemctlStub(t, preflightBin, filepath.Join(tmpPreflight, "svc.log"))
	preflight := remotePreflightScript(SyncCopyOptions{RemoteDir: realRemote, RemoteService: "orchestrator"})
	out, err := runShellWithPath(preflightBin, preflight)
	if err != nil {
		t.Fatalf("preflight script failed: %v\n%s", err, string(out))
	}
	probe, err := parseRemoteProbe(string(out))
	if err != nil {
		t.Fatalf("parseRemoteProbe failed: %v\n%s", err, string(out))
	}
	if !probe.configPresent {
		t.Fatalf("expected configPresent=true")
	}
	if err := checkSyncLayoutBytes(realRemote, probe.configRaw, []string{"events", "queues"}, true); err == nil {
		t.Fatal("expected checkSyncLayoutBytes to reject passphrase inside events/")
	}
}

func TestDefaultSSHRunExcludesStdoutSecretsAndFlagsPreDispatchFailure(t *testing.T) {
	binDir := t.TempDir()
	secretPayload := "SECRET_CONFIG_BASE64_PAYLOAD_DO_NOT_LEAK"
	writeExecScript(t, filepath.Join(binDir, "ssh"), fmt.Sprintf(`#!/bin/sh
echo "CONFIG_B64_BEGIN"
echo %s
echo "CONFIG_B64_END"
echo "remote systemctl failure" >&2
exit 1
`, sshQuote(secretPayload)))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	opts := SyncCopyOptions{Host: "root@target.example", Port: 22}
	stdout, err := defaultSSHRun(context.Background(), opts, "true")
	if err == nil {
		t.Fatal("expected ssh error")
	}
	if errors.Is(err, errSSHNotStarted) {
		t.Fatalf("dispatched ssh exit failure must not be flagged as errSSHNotStarted: %v", err)
	}
	if !strings.Contains(err.Error(), "remote systemctl failure") {
		t.Fatalf("error should contain stderr message: %v", err)
	}
	if strings.Contains(err.Error(), secretPayload) {
		t.Fatalf("error must never leak stdout config payload: %v", err)
	}
	if !strings.Contains(stdout, secretPayload) {
		t.Fatalf("stdout return value should remain available separately: %q", stdout)
	}

	// Pre-cancelled context prevents ssh process from starting -> flagged as errSSHNotStarted.
	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = defaultSSHRun(cancelCtx, opts, "true")
	if !errors.Is(err, errSSHNotStarted) {
		t.Fatalf("expected errSSHNotStarted for pre-cancelled context, got %v", err)
	}

	// Inherited pipe held open by a grandchild (e.g. blocked ProxyCommand) is bounded by sshWaitDelay.
	oldWaitDelay := sshWaitDelay
	sshWaitDelay = 150 * time.Millisecond
	t.Cleanup(func() { sshWaitDelay = oldWaitDelay })
	writeExecScript(t, filepath.Join(binDir, "ssh"), `#!/bin/sh
(sleep 30) &
sleep 30
`)
	shortCtx, shortCancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer shortCancel()
	start := time.Now()
	_, err = defaultSSHRun(shortCtx, opts, "true")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected timeout error from hanging ssh helper")
	}
	if elapsed > 3*time.Second {
		t.Fatalf("defaultSSHRun blocked too long on inherited pipe (%s)", elapsed)
	}
}

func TestCopySyncDataCutoverPreDispatchCleanupVsPostDispatchPreserve(t *testing.T) {
	dir := populateOrchestratorDir(t)
	cfg := config.Default()
	cfg.OrchestratorDir = dir

	oldSSH, oldRsync, oldTool := sshRun, rsyncRun, toolExists
	t.Cleanup(func() { sshRun, rsyncRun, toolExists = oldSSH, oldRsync, oldTool })
	toolExists = func(string) bool { return true }

	t.Run("cancelled after rsync restarts local before remote cleanup with bounded context", func(t *testing.T) {
		calls := stubDeployHost(t, true, service.Active, filepath.Join(t.TempDir(), "orchestrator.service"))
		ctx, cancel := context.WithCancel(context.Background())
		var events []string
		oldStart := startService
		startService = func(name string) error {
			events = append(events, "start-local")
			return oldStart(name)
		}
		var cleanupRan bool
		var cleanupHasDeadline bool
		var cleanupCtxErr error
		var stagedPath string
		sshCalls := 0
		sshRun = func(runCtx context.Context, _ SyncCopyOptions, script string) (string, error) {
			sshCalls++
			if sshCalls == 1 {
				return "CONFIG_PRESENT=0\nLOAD=loaded\nACTIVE=active\n", nil
			}
			if strings.HasPrefix(script, "rm -rf ") {
				events = append(events, "cleanup-remote")
				cleanupRan = strings.Contains(script, stagedPath)
				_, cleanupHasDeadline = runCtx.Deadline()
				cleanupCtxErr = runCtx.Err()
			}
			return "", nil
		}
		rsyncRun = func(_ context.Context, _ SyncCopyOptions, _ []string, remoteStage string) error {
			stagedPath = remoteStage
			cancel()
			return nil
		}

		if _, err := CopySyncData(ctx, cfg, SyncCopyOptions{Host: "root@target.example"}); err == nil {
			t.Fatal("expected cancellation error")
		}
		if !cleanupRan || !cleanupHasDeadline || cleanupCtxErr != nil {
			t.Fatalf("expected bounded live cleanup of %q (ran=%v deadline=%v ctxErr=%v)", stagedPath, cleanupRan, cleanupHasDeadline, cleanupCtxErr)
		}
		if strings.Join(events, ",") != "start-local,cleanup-remote" {
			t.Fatalf("local restart must precede remote cleanup, got %v", events)
		}
		if strings.Join(*calls, ",") != "stop orchestrator,start orchestrator" {
			t.Fatalf("local service must restart after pre-cutover cancellation: %v", *calls)
		}
	})

	t.Run("confirmed ssh pre-dispatch error cleans up stage", func(t *testing.T) {
		calls := stubDeployHost(t, true, service.Active, filepath.Join(t.TempDir(), "orchestrator.service"))
		var cleanupRan bool
		var stagedPath string
		sshCalls := 0
		sshRun = func(_ context.Context, _ SyncCopyOptions, script string) (string, error) {
			sshCalls++
			switch {
			case sshCalls == 1:
				return "CONFIG_PRESENT=0\nLOAD=loaded\nACTIVE=active\n", nil
			case strings.Contains(script, "REMOTE_RESTARTED="):
				return "", fmt.Errorf("%w: context canceled", errSSHNotStarted)
			case strings.HasPrefix(script, "rm -rf "):
				cleanupRan = strings.Contains(script, stagedPath)
			}
			return "", nil
		}
		rsyncRun = func(_ context.Context, _ SyncCopyOptions, _ []string, remoteStage string) error {
			stagedPath = remoteStage
			return nil
		}

		if _, err := CopySyncData(context.Background(), cfg, SyncCopyOptions{Host: "root@target.example"}); err == nil {
			t.Fatal("expected cutover pre-dispatch error")
		}
		if !cleanupRan {
			t.Fatalf("expected remote stage %q to be cleaned up on errSSHNotStarted", stagedPath)
		}
		if strings.Join(*calls, ",") != "stop orchestrator,start orchestrator" {
			t.Fatalf("local service must restart: %v", *calls)
		}
	})

	t.Run("post-dispatch cutover error preserves remote recovery artifacts", func(t *testing.T) {
		calls := stubDeployHost(t, true, service.Active, filepath.Join(t.TempDir(), "orchestrator.service"))
		errPostDispatchCutover := errors.New("exit status 1: rollback incomplete")
		cleanupRan := false
		sshCalls := 0
		sshRun = func(_ context.Context, _ SyncCopyOptions, script string) (string, error) {
			sshCalls++
			switch {
			case sshCalls == 1:
				return "CONFIG_PRESENT=0\nLOAD=loaded\nACTIVE=active\n", nil
			case strings.Contains(script, "REMOTE_RESTARTED="):
				return "", errPostDispatchCutover
			case strings.HasPrefix(script, "rm -rf "):
				cleanupRan = true
			}
			return "", nil
		}
		rsyncRun = func(_ context.Context, _ SyncCopyOptions, _ []string, _ string) error { return nil }

		_, err := CopySyncData(context.Background(), cfg, SyncCopyOptions{Host: "root@target.example"})
		if !errors.Is(err, errPostDispatchCutover) {
			t.Fatalf("expected sentinel post-dispatch cutover error, got %v", err)
		}
		if cleanupRan {
			t.Fatal("post-dispatch cutover error must NOT delete remote recovery artifacts")
		}
		if strings.Join(*calls, ",") != "stop orchestrator,start orchestrator" {
			t.Fatalf("local service must restart: %v", *calls)
		}
	})
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

func writeExecScript(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeDefaultSystemctlStub(t *testing.T, binDir, logFile string) {
	t.Helper()
	writeExecScript(t, filepath.Join(binDir, "systemctl"), fmt.Sprintf(`#!/bin/sh
case "$1" in
  show)
    case "$*" in
      *LoadState*) echo not-found ;;
      *ActiveState*) echo inactive ;;
      *) echo "" ;;
    esac
    ;;
  stop|start)
    echo "$1 $2" >> %s
    exit 0
    ;;
  *)
    exit 0
    ;;
esac
`, sshQuote(logFile)))
}

func runShellWithPath(pathPrefix, script string) ([]byte, error) {
	cmd := exec.Command("sh", "-eu", "-c", script)
	if pathPrefix != "" {
		cmd.Env = append(os.Environ(), "PATH="+pathPrefix+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	return cmd.CombinedOutput()
}

func TestRemoteCutoverScriptRollbackAndCleanup(t *testing.T) {
	realMV, err := exec.LookPath("mv")
	if err != nil {
		t.Fatal(err)
	}

	// 1. Fresh remote: events succeeds, queues is missing in stage -> verify mv actually
	//    moved events/ into place before rollback removed installed events/, stage, and backup.
	t.Run("fresh rollback traces first move before cleanup", func(t *testing.T) {
		tmp := t.TempDir()
		remoteDir := filepath.Join(tmp, ".orchestrator")
		stageDir := filepath.Join(remoteDir, ".sync-copy-stage-1")
		backupDir := filepath.Join(remoteDir, "sync-backup-before-copy-1")
		if err := os.MkdirAll(filepath.Join(stageDir, "events"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(stageDir, "events", "data"), []byte("new"), 0o600); err != nil {
			t.Fatal(err)
		}
		traceFile := filepath.Join(tmp, "mv.trace")
		svcLog := filepath.Join(tmp, "svc.log")
		binDir := filepath.Join(tmp, "bin")
		if err := os.MkdirAll(binDir, 0o755); err != nil {
			t.Fatal(err)
		}
		writeDefaultSystemctlStub(t, binDir, svcLog)
		writeExecScript(t, filepath.Join(binDir, "mv"), fmt.Sprintf(`#!/bin/sh
%s "$@"
rc=$?
if [ "$rc" -eq 0 ]; then
  printf '%%s -> %%s\n' "$1" "$2" >> %s
fi
exit "$rc"
`, sshQuote(realMV), sshQuote(traceFile)))

		script := remoteCutoverScript(SyncCopyOptions{
			RemoteDir:     remoteDir,
			RemoteService: "orchestrator",
		}, stageDir, backupDir, []string{"events", "queues"}, "")
		if out, err := runShellWithPath(binDir, script); err == nil {
			t.Fatalf("expected cutover to fail on missing staged queues, output:\n%s", string(out))
		}
		if _, err := os.Stat(svcLog); !errors.Is(err, os.ErrNotExist) {
			b, _ := os.ReadFile(svcLog)
			t.Fatalf("stub systemctl should not stop/start absent unit, got %q", string(b))
		}
		traceBytes, err := os.ReadFile(traceFile)
		if err != nil {
			t.Fatalf("expected mv trace proving swap began: %v", err)
		}
		wantMove := filepath.Join(stageDir, "events") + " -> " + filepath.Join(remoteDir, "events")
		if !strings.Contains(string(traceBytes), wantMove) {
			t.Fatalf("expected first move %q in trace, got:\n%s", wantMove, string(traceBytes))
		}
		for _, gone := range []string{filepath.Join(remoteDir, "events"), stageDir, backupDir} {
			if _, err := os.Stat(gone); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("expected %s to be cleaned up on rollback, stat err=%v", gone, err)
			}
		}
	})

	// 2. Pre-swap backup mkdir collision: stage dir is cleaned up, existing backup dir preserved.
	t.Run("backup collision preserves existing backup", func(t *testing.T) {
		tmp := t.TempDir()
		remoteDir2 := filepath.Join(tmp, ".orchestrator")
		stageDir2 := filepath.Join(remoteDir2, ".sync-copy-stage-2")
		backupDir2 := filepath.Join(remoteDir2, "sync-backup-before-copy-2")
		if err := os.MkdirAll(stageDir2, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(backupDir2, 0o700); err != nil {
			t.Fatal(err)
		}
		binDir := filepath.Join(tmp, "bin")
		if err := os.MkdirAll(binDir, 0o755); err != nil {
			t.Fatal(err)
		}
		writeDefaultSystemctlStub(t, binDir, filepath.Join(tmp, "svc.log"))
		script2 := remoteCutoverScript(SyncCopyOptions{
			RemoteDir:     remoteDir2,
			RemoteService: "orchestrator",
		}, stageDir2, backupDir2, []string{"events"}, "")
		if out, err := runShellWithPath(binDir, script2); err == nil {
			t.Fatalf("expected cutover to fail on colliding backup dir, output:\n%s", string(out))
		}
		if _, err := os.Stat(stageDir2); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("expected stageDir2 cleaned up, err=%v", err)
		}
		if _, err := os.Stat(backupDir2); err != nil {
			t.Fatalf("existing colliding backupDir2 should be preserved, err=%v", err)
		}
	})

	// 3. Stage creation refuses colliding stage rather than rm -rf'ing another copy's stage.
	t.Run("stage create refuses collision", func(t *testing.T) {
		remoteDir := filepath.Join(t.TempDir(), ".orchestrator")
		stageDir := filepath.Join(remoteDir, ".sync-copy-stage-existing")
		if err := os.MkdirAll(stageDir, 0o700); err != nil {
			t.Fatal(err)
		}
		marker := filepath.Join(stageDir, "keep")
		if err := os.WriteFile(marker, []byte("other-host"), 0o600); err != nil {
			t.Fatal(err)
		}
		script := remoteStageCreateScript(SyncCopyOptions{RemoteDir: remoteDir}, stageDir)
		if out, err := runShellWithPath("", script); err == nil {
			t.Fatalf("expected colliding stage create to fail, output:\n%s", string(out))
		}
		if got, err := os.ReadFile(marker); err != nil || string(got) != "other-host" {
			t.Fatalf("existing stage contents must be preserved, got %q (err=%v)", string(got), err)
		}
	})

	// 4. Concurrent cutovers on the same destination serialize via destination lock.
	t.Run("concurrent cutovers serialize without interleaving", func(t *testing.T) {
		tmp := t.TempDir()
		remoteDir := filepath.Join(tmp, ".orchestrator")
		stage1 := filepath.Join(remoteDir, ".sync-copy-stage-a")
		stage2 := filepath.Join(remoteDir, ".sync-copy-stage-b")
		backup1 := filepath.Join(remoteDir, "sync-backup-before-copy-a")
		backup2 := filepath.Join(remoteDir, "sync-backup-before-copy-b")
		for _, s := range []struct {
			dir  string
			body string
		}{{stage1, "copy-1"}, {stage2, "copy-2"}} {
			if err := os.MkdirAll(filepath.Join(s.dir, "events"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(s.dir, "events", "val"), []byte(s.body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		binDir := filepath.Join(tmp, "bin")
		if err := os.MkdirAll(binDir, 0o755); err != nil {
			t.Fatal(err)
		}
		writeDefaultSystemctlStub(t, binDir, filepath.Join(tmp, "svc.log"))
		activeFlag := filepath.Join(tmp, "mv-active")
		overlapFlag := filepath.Join(tmp, "mv-overlap")
		writeExecScript(t, filepath.Join(binDir, "mv"), fmt.Sprintf(`#!/bin/sh
if ! mkdir %s 2>/dev/null; then
  echo overlap >> %s
fi
sleep 0.2
%s "$@"
rc=$?
rmdir %s 2>/dev/null || true
exit "$rc"
`, sshQuote(activeFlag), sshQuote(overlapFlag), sshQuote(realMV), sshQuote(activeFlag)))

		s1 := remoteCutoverScript(SyncCopyOptions{RemoteDir: remoteDir, RemoteService: "orchestrator"}, stage1, backup1, []string{"events"}, "")
		s2 := remoteCutoverScript(SyncCopyOptions{RemoteDir: remoteDir, RemoteService: "orchestrator"}, stage2, backup2, []string{"events"}, "")

		var wg sync.WaitGroup
		errs := make([]error, 2)
		outs := make([][]byte, 2)
		for i, sc := range []string{s1, s2} {
			wg.Add(1)
			go func(idx int, script string) {
				defer wg.Done()
				outs[idx], errs[idx] = runShellWithPath(binDir, script)
			}(i, sc)
		}
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Fatalf("concurrent cutover %d failed: %v\n%s", i, err, string(outs[i]))
			}
		}
		if _, err := os.Stat(overlapFlag); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("concurrent cutovers overlapped inside live swap")
		}
		val, err := os.ReadFile(filepath.Join(remoteDir, "events", "val"))
		if err != nil || (string(val) != "copy-1" && string(val) != "copy-2") {
			t.Fatalf("unexpected final events/val = %q (err=%v)", string(val), err)
		}
	})

	// 5. Systemd unit discovered at cutover time is stopped and restarted cleanly.
	t.Run("unit discovered at cutover is stopped and restarted", func(t *testing.T) {
		tmp := t.TempDir()
		remoteDir := filepath.Join(tmp, ".orchestrator")
		stageDir := filepath.Join(remoteDir, ".sync-copy-stage-unit")
		backupDir := filepath.Join(remoteDir, "sync-backup-before-copy-unit")
		if err := os.MkdirAll(filepath.Join(remoteDir, "events"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(remoteDir, "events", "v"), []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(stageDir, "events"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(stageDir, "events", "v"), []byte("new"), 0o600); err != nil {
			t.Fatal(err)
		}
		binDir := filepath.Join(tmp, "bin")
		if err := os.MkdirAll(binDir, 0o755); err != nil {
			t.Fatal(err)
		}
		stateFile := filepath.Join(tmp, "svc.state")
		logFile := filepath.Join(tmp, "svc.log")
		if err := os.WriteFile(stateFile, []byte("active"), 0o600); err != nil {
			t.Fatal(err)
		}
		writeExecScript(t, filepath.Join(binDir, "systemctl"), fmt.Sprintf(`#!/bin/sh
case "$1" in
  show)
    case "$*" in
      *LoadState*) echo loaded ;;
      *ActiveState*) cat %s ;;
    esac
    ;;
  stop)
    echo "stop $2" >> %s
    echo inactive > %s
    ;;
  start)
    echo "start $2" >> %s
    echo active > %s
    ;;
esac
`, sshQuote(stateFile), sshQuote(logFile), sshQuote(stateFile), sshQuote(logFile), sshQuote(stateFile)))

		script := remoteCutoverScript(SyncCopyOptions{RemoteDir: remoteDir, RemoteService: "orchestrator"}, stageDir, backupDir, []string{"events"}, "")
		out, err := runShellWithPath(binDir, script)
		if err != nil {
			t.Fatalf("cutover failed: %v\n%s", err, string(out))
		}
		unitFound, restarted, err := parseRemoteCutoverResult(string(out))
		if err != nil || !unitFound || !restarted {
			t.Fatalf("parseRemoteCutoverResult = (%v, %v, %v), output:\n%s", unitFound, restarted, err, string(out))
		}
		logBytes, _ := os.ReadFile(logFile)
		if strings.TrimSpace(string(logBytes)) != "stop orchestrator\nstart orchestrator" {
			t.Fatalf("systemctl log = %q", string(logBytes))
		}
		if got, _ := os.ReadFile(filepath.Join(remoteDir, "events", "v")); string(got) != "new" {
			t.Fatalf("live events = %q, want new", string(got))
		}
	})

	// 6. ActiveState query failure aborts before systemctl stop.
	t.Run("ActiveState query failure aborts before stop", func(t *testing.T) {
		tmp := t.TempDir()
		remoteDir := filepath.Join(tmp, ".orchestrator")
		stageDir := filepath.Join(remoteDir, ".sync-copy-stage-err")
		backupDir := filepath.Join(remoteDir, "sync-backup-before-copy-err")
		if err := os.MkdirAll(filepath.Join(remoteDir, "events"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(remoteDir, "events", "v"), []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(stageDir, "events"), 0o700); err != nil {
			t.Fatal(err)
		}
		binDir := filepath.Join(tmp, "bin")
		if err := os.MkdirAll(binDir, 0o755); err != nil {
			t.Fatal(err)
		}
		logFile := filepath.Join(tmp, "svc.log")
		writeExecScript(t, filepath.Join(binDir, "systemctl"), fmt.Sprintf(`#!/bin/sh
case "$1" in
  show)
    case "$*" in
      *LoadState*) echo loaded ;;
      *ActiveState*) echo "dbus error" >&2; exit 1 ;;
    esac
    ;;
  *)
    echo "$1 $2" >> %s
    ;;
esac
`, sshQuote(logFile)))

		script := remoteCutoverScript(SyncCopyOptions{RemoteDir: remoteDir, RemoteService: "orchestrator"}, stageDir, backupDir, []string{"events"}, "")
		if out, err := runShellWithPath(binDir, script); err == nil {
			t.Fatalf("expected ActiveState failure to abort cutover, output:\n%s", string(out))
		}
		if _, err := os.Stat(logFile); !errors.Is(err, os.ErrNotExist) {
			b, _ := os.ReadFile(logFile)
			t.Fatalf("systemctl stop/start must not run on ActiveState failure, log=%q", string(b))
		}
		if got, _ := os.ReadFile(filepath.Join(remoteDir, "events", "v")); string(got) != "old" {
			t.Fatalf("live events changed on ActiveState failure: %q", string(got))
		}
	})

	// 7. Remote config.json changed during rsync aborts before stopping service.
	t.Run("config change during rsync aborts before stop", func(t *testing.T) {
		tmp := t.TempDir()
		remoteDir := filepath.Join(tmp, ".orchestrator")
		stageDir := filepath.Join(remoteDir, ".sync-copy-stage-cfg")
		backupDir := filepath.Join(remoteDir, "sync-backup-before-copy-cfg")
		if err := os.MkdirAll(filepath.Join(remoteDir, "events"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(ConfigPath(remoteDir), []byte(`{"ProducerKeyFilePassphraseFile":"events/secret"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(stageDir, "events"), 0o700); err != nil {
			t.Fatal(err)
		}
		binDir := filepath.Join(tmp, "bin")
		if err := os.MkdirAll(binDir, 0o755); err != nil {
			t.Fatal(err)
		}
		logFile := filepath.Join(tmp, "svc.log")
		writeExecScript(t, filepath.Join(binDir, "systemctl"), fmt.Sprintf(`#!/bin/sh
echo "$*" >> %s
`, sshQuote(logFile)))

		oldSHA := strings.Repeat("0", 64)
		script := remoteCutoverScript(SyncCopyOptions{RemoteDir: remoteDir, RemoteService: "orchestrator"}, stageDir, backupDir, []string{"events"}, oldSHA)
		out, err := runShellWithPath(binDir, script)
		if err == nil || !strings.Contains(string(out), "config changed during sync copy") {
			t.Fatalf("expected config change abort, err=%v output:\n%s", err, string(out))
		}
		if _, err := os.Stat(logFile); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("systemctl must not be called when remote config changed")
		}
	})

	// 8. Remote startup failure after swap confirms stopped, restores old dirs, and restarts on restored data.
	t.Run("startup failure triggers rollback and recovery restart", func(t *testing.T) {
		tmp := t.TempDir()
		remoteDir := filepath.Join(tmp, ".orchestrator")
		stageDir := filepath.Join(remoteDir, ".sync-copy-stage-startfail")
		backupDir := filepath.Join(remoteDir, "sync-backup-before-copy-startfail")
		if err := os.MkdirAll(filepath.Join(remoteDir, "events"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(remoteDir, "events", "v"), []byte("old-good"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(stageDir, "events"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(stageDir, "events", "v"), []byte("new-bad"), 0o600); err != nil {
			t.Fatal(err)
		}
		binDir := filepath.Join(tmp, "bin")
		if err := os.MkdirAll(binDir, 0o755); err != nil {
			t.Fatal(err)
		}
		stateFile := filepath.Join(tmp, "svc.state")
		logFile := filepath.Join(tmp, "svc.log")
		startCountFile := filepath.Join(tmp, "start.count")
		if err := os.WriteFile(stateFile, []byte("active"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(startCountFile, []byte("0"), 0o600); err != nil {
			t.Fatal(err)
		}
		writeExecScript(t, filepath.Join(binDir, "systemctl"), fmt.Sprintf(`#!/bin/sh
case "$1" in
  show)
    case "$*" in
      *LoadState*) echo loaded ;;
      *ActiveState*) cat %s ;;
    esac
    ;;
  stop)
    echo "stop $2" >> %s
    echo inactive > %s
    ;;
  start)
    n=$(cat %s)
    n=$((n + 1))
    echo "$n" > %s
    echo "start#$n $2" >> %s
    if [ "$n" -eq 1 ]; then
      echo "bad replacement data" >&2
      echo failed > %s
      exit 1
    fi
    echo active > %s
    ;;
esac
`, sshQuote(stateFile), sshQuote(logFile), sshQuote(stateFile),
			sshQuote(startCountFile), sshQuote(startCountFile), sshQuote(logFile),
			sshQuote(stateFile), sshQuote(stateFile)))

		script := remoteCutoverScript(SyncCopyOptions{RemoteDir: remoteDir, RemoteService: "orchestrator"}, stageDir, backupDir, []string{"events"}, "")
		out, err := runShellWithPath(binDir, script)
		if err == nil {
			t.Fatalf("expected startup failure to return error, output:\n%s", string(out))
		}
		if !strings.Contains(string(out), "bad replacement data") {
			t.Fatalf("original startup error must be preserved, output:\n%s", string(out))
		}
		if got, _ := os.ReadFile(filepath.Join(remoteDir, "events", "v")); string(got) != "old-good" {
			t.Fatalf("live events = %q, want old-good restored after startup failure", string(got))
		}
		logBytes, _ := os.ReadFile(logFile)
		wantLog := "stop orchestrator\nstart#1 orchestrator\nstop orchestrator\nstart#2 orchestrator"
		if strings.TrimSpace(string(logBytes)) != wantLog {
			t.Fatalf("systemctl log =\n%s\nwant:\n%s", strings.TrimSpace(string(logBytes)), wantLog)
		}
	})

	// 9. Incomplete rollback must keep service stopped and preserve backup path.
	t.Run("incomplete rollback keeps unit stopped and preserves backup", func(t *testing.T) {
		tmp := t.TempDir()
		remoteDir := filepath.Join(tmp, ".orchestrator")
		stageDir := filepath.Join(remoteDir, ".sync-copy-stage-incomp")
		backupDir := filepath.Join(remoteDir, "sync-backup-before-copy-incomp")
		if err := os.MkdirAll(filepath.Join(remoteDir, "events"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(remoteDir, "events", "v"), []byte("old-events"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(stageDir, "events"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(stageDir, "events", "v"), []byte("new-events"), 0o600); err != nil {
			t.Fatal(err)
		}
		// queues intentionally missing in stageDir so swap fails after events succeeds.
		binDir := filepath.Join(tmp, "bin")
		if err := os.MkdirAll(binDir, 0o755); err != nil {
			t.Fatal(err)
		}
		stateFile := filepath.Join(tmp, "svc.state")
		logFile := filepath.Join(tmp, "svc.log")
		if err := os.WriteFile(stateFile, []byte("active"), 0o600); err != nil {
			t.Fatal(err)
		}
		writeExecScript(t, filepath.Join(binDir, "systemctl"), fmt.Sprintf(`#!/bin/sh
case "$1" in
  show)
    case "$*" in
      *LoadState*) echo loaded ;;
      *ActiveState*) cat %s ;;
    esac
    ;;
  stop)
    echo "stop $2" >> %s
    echo inactive > %s
    ;;
  start)
    echo "start $2" >> %s
    echo active > %s
    ;;
esac
`, sshQuote(stateFile), sshQuote(logFile), sshQuote(stateFile), sshQuote(logFile), sshQuote(stateFile)))
		// Fail mv only when restoring from backupDir back into remoteDir.
		writeExecScript(t, filepath.Join(binDir, "mv"), fmt.Sprintf(`#!/bin/sh
case "$1" in
  %s/*)
    echo "simulated restore IO error" >&2
    exit 1
    ;;
esac
exec %s "$@"
`, sshQuote(backupDir), sshQuote(realMV)))

		script := remoteCutoverScript(SyncCopyOptions{RemoteDir: remoteDir, RemoteService: "orchestrator"}, stageDir, backupDir, []string{"events", "queues"}, "")
		out, err := runShellWithPath(binDir, script)
		if err == nil {
			t.Fatalf("expected cutover error, output:\n%s", string(out))
		}
		if !strings.Contains(string(out), backupDir) {
			t.Fatalf("stderr should report preserved backup path %q:\n%s", backupDir, string(out))
		}
		logBytes, _ := os.ReadFile(logFile)
		if strings.TrimSpace(string(logBytes)) != "stop orchestrator" {
			t.Fatalf("unit must stay stopped after incomplete rollback, log = %q", string(logBytes))
		}
		if got, err := os.ReadFile(filepath.Join(backupDir, "events", "v")); err != nil || string(got) != "old-events" {
			t.Fatalf("backup must be preserved on incomplete rollback, got %q (err=%v)", string(got), err)
		}
	})

	// 10. Startup failure followed by failed stop during rollback aborts restore and preserves backup.
	t.Run("startup failure with failed rollback stop preserves backup and skips restore", func(t *testing.T) {
		tmp := t.TempDir()
		remoteDir := filepath.Join(tmp, ".orchestrator")
		stageDir := filepath.Join(remoteDir, ".sync-copy-stage-stopfail")
		backupDir := filepath.Join(remoteDir, "sync-backup-before-copy-stopfail")
		if err := os.MkdirAll(filepath.Join(remoteDir, "events"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(remoteDir, "events", "v"), []byte("old-good"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(stageDir, "events"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(stageDir, "events", "v"), []byte("new-bad"), 0o600); err != nil {
			t.Fatal(err)
		}
		binDir := filepath.Join(tmp, "bin")
		if err := os.MkdirAll(binDir, 0o755); err != nil {
			t.Fatal(err)
		}
		stateFile := filepath.Join(tmp, "svc.state")
		logFile := filepath.Join(tmp, "svc.log")
		stopCountFile := filepath.Join(tmp, "stop.count")
		if err := os.WriteFile(stateFile, []byte("active"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(stopCountFile, []byte("0"), 0o600); err != nil {
			t.Fatal(err)
		}
		writeExecScript(t, filepath.Join(binDir, "systemctl"), fmt.Sprintf(`#!/bin/sh
case "$1" in
  show)
    case "$*" in
      *LoadState*) echo loaded ;;
      *ActiveState*) cat %s ;;
    esac
    ;;
  stop)
    n=$(cat %s)
    n=$((n + 1))
    echo "$n" > %s
    echo "stop#$n $2" >> %s
    if [ "$n" -eq 2 ]; then
      echo "still stopping" >&2
      echo active > %s
      exit 1
    fi
    echo inactive > %s
    ;;
  start)
    echo "start $2" >> %s
    echo "bad replacement data" >&2
    echo active > %s
    exit 1
    ;;
esac
`, sshQuote(stateFile), sshQuote(stopCountFile), sshQuote(stopCountFile), sshQuote(logFile),
			sshQuote(stateFile), sshQuote(stateFile), sshQuote(logFile), sshQuote(stateFile)))

		script := remoteCutoverScript(SyncCopyOptions{RemoteDir: remoteDir, RemoteService: "orchestrator"}, stageDir, backupDir, []string{"events"}, "")
		out, err := runShellWithPath(binDir, script)
		if err == nil {
			t.Fatalf("expected startup failure, output:\n%s", string(out))
		}
		for _, want := range []string{"bad replacement data", "could not be confirmed stopped", backupDir} {
			if !strings.Contains(string(out), want) {
				t.Errorf("output missing %q:\n%s", want, string(out))
			}
		}
		// Must not overwrite live events while service may still be running, and must preserve backup.
		if got, _ := os.ReadFile(filepath.Join(remoteDir, "events", "v")); string(got) != "new-bad" {
			t.Fatalf("live events = %q, want new-bad untouched when rollback stop failed", string(got))
		}
		if got, _ := os.ReadFile(filepath.Join(backupDir, "events", "v")); string(got) != "old-good" {
			t.Fatalf("backup events = %q, want old-good preserved", string(got))
		}
		logBytes, _ := os.ReadFile(logFile)
		wantLog := "stop#1 orchestrator\nstart orchestrator\nstop#2 orchestrator"
		if strings.TrimSpace(string(logBytes)) != wantLog {
			t.Fatalf("systemctl log =\n%s\nwant:\n%s", strings.TrimSpace(string(logBytes)), wantLog)
		}
	})

	// 11. Closed stdout/stderr pipe (disconnected SSH client) must still run on_exit rollback
	//     and restart recovery instead of dying on SIGPIPE.
	t.Run("closed stdout and stderr pipe still rolls back and restarts", func(t *testing.T) {
		tmp := t.TempDir()
		remoteDir := filepath.Join(tmp, ".orchestrator")
		stageDir := filepath.Join(remoteDir, ".sync-copy-stage-sigpipe")
		backupDir := filepath.Join(remoteDir, "sync-backup-before-copy-sigpipe")
		lockDir := filepath.Join(remoteDir, ".nomctl-sync-cutover.lockdir")
		if err := os.MkdirAll(filepath.Join(remoteDir, "events"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(remoteDir, "events", "v"), []byte("old-good"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(stageDir, "events"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(stageDir, "events", "v"), []byte("new-bad"), 0o600); err != nil {
			t.Fatal(err)
		}
		binDir := filepath.Join(tmp, "bin")
		if err := os.MkdirAll(binDir, 0o755); err != nil {
			t.Fatal(err)
		}
		stateFile := filepath.Join(tmp, "svc.state")
		startCountFile := filepath.Join(tmp, "start.count")
		if err := os.WriteFile(stateFile, []byte("active"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(startCountFile, []byte("0"), 0o600); err != nil {
			t.Fatal(err)
		}
		// Simulate first start leaving the service stuck in activating (triggers stderr echo
		// onto the closed pipe), and recovery start returning to active.
		writeExecScript(t, filepath.Join(binDir, "systemctl"), fmt.Sprintf(`#!/bin/sh
case "$1" in
  show)
    case "$*" in
      *LoadState*) echo loaded ;;
      *ActiveState*) cat %s ;;
    esac
    ;;
  stop)
    echo inactive > %s
    ;;
  start)
    n=$(cat %s)
    n=$((n + 1))
    echo "$n" > %s
    if [ "$n" -eq 1 ]; then
      echo activating > %s
    else
      echo active > %s
    fi
    ;;
esac
`, sshQuote(stateFile), sshQuote(stateFile), sshQuote(startCountFile), sshQuote(startCountFile), sshQuote(stateFile), sshQuote(stateFile)))

		script := remoteCutoverScript(SyncCopyOptions{RemoteDir: remoteDir, RemoteService: "orchestrator"}, stageDir, backupDir, []string{"events"}, "")
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		// Close read end before running so every stdout/stderr write hits EPIPE/SIGPIPE.
		if err := r.Close(); err != nil {
			_ = w.Close()
			t.Fatal(err)
		}
		cmd := exec.Command("sh", "-eu", "-c", script)
		cmd.Env = append(os.Environ(), "PATH="+binDir+":"+os.Getenv("PATH"))
		cmd.Stdout = w
		cmd.Stderr = w
		runErr := cmd.Run()
		_ = w.Close()
		if runErr == nil {
			t.Fatal("expected cutover to fail when first start stays activating with closed pipes")
		}
		if got, err := os.ReadFile(filepath.Join(remoteDir, "events", "v")); err != nil || string(got) != "old-good" {
			t.Fatalf("old data not restored after closed-pipe failure: got %q (err=%v)", string(got), err)
		}
		if got, err := os.ReadFile(stateFile); err != nil || strings.TrimSpace(string(got)) != "active" {
			t.Fatalf("service not restarted to active after closed-pipe rollback: got %q (err=%v)", string(got), err)
		}
		for _, gone := range []string{stageDir, backupDir, lockDir} {
			if _, err := os.Stat(gone); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("expected %s to be cleaned up after closed-pipe rollback, stat err=%v", gone, err)
			}
		}
	})
}
