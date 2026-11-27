package orchestrator

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/0x3639/nomctl/internal/backup"
	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/execx"
	"github.com/0x3639/nomctl/internal/logx"
)

// SyncBackupDir returns the default directory for orchestrator sync backups.
func SyncBackupDir(cfg config.Config) string {
	return filepath.Join(cfg.BackupDir, "orchestrator")
}

// SyncBackupOptions configures BackupSyncData.
type SyncBackupOptions struct {
	OutputDir string
	Now       func() time.Time
}

// SyncBackupResult describes a completed sync-data backup archive.
type SyncBackupResult struct {
	ArchivePath  string
	HashPath     string
	SHA256       string
	IncludedDirs []string
	Bytes        int64
}

// BackupSyncData stops the orchestrator if running, copies events/ and queues/
// into a private staging directory, restarts the orchestrator before
// compression, and publishes a .tar.gz archive plus .hash sidecar.
// Configuration, producer keys, TSS shares, and voting state are never copied.
func BackupSyncData(ctx context.Context, cfg config.Config, opts SyncBackupOptions) (SyncBackupResult, error) {
	var res SyncBackupResult
	dir, err := ValidateDir(cfg.OrchestratorDir)
	if err != nil {
		return res, err
	}
	ok, err := Installed(cfg)
	if err != nil {
		return res, err
	}
	if !ok {
		return res, ErrNotInstalled
	}
	if err := checkSyncLayout(dir); err != nil {
		return res, err
	}
	present, err := existingSyncDirs(dir)
	if err != nil {
		return res, err
	}
	if len(present) == 0 {
		return res, fmt.Errorf("no sync data directories (%s) found under %s", strings.Join(SyncDirs, ", "), dir)
	}

	outDir := strings.TrimSpace(opts.OutputDir)
	if outDir == "" {
		outDir = SyncBackupDir(cfg)
	}
	outDir, err = ValidateDir(outDir)
	if err != nil {
		return res, fmt.Errorf("invalid backup output directory: %w", err)
	}
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		return res, fmt.Errorf("create backup directory %s: %w", outDir, err)
	}

	nowFn := opts.Now
	if nowFn == nil {
		nowFn = time.Now
	}

	st, err := serviceStatus(cfg.OrchestratorService)
	if err != nil {
		return res, err
	}
	wasRunning := st.Running()

	stageDir, err := os.MkdirTemp(outDir, ".orchestrator-sync-stage-")
	if err != nil {
		return res, err
	}
	keepStage := false
	defer func() {
		if !keepStage {
			_ = os.RemoveAll(stageDir)
		}
	}()

	slog.Info("Stopping " + cfg.OrchestratorService + " to capture consistent sync data…")
	if err := stopService(cfg.OrchestratorService); err != nil {
		return res, err
	}

	var copied []string
	var copyErr error
	for _, name := range SyncDirs {
		src := filepath.Join(dir, name)
		if _, err := os.Lstat(src); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			copyErr = err
			break
		}
		if err := copySafeTree(ctx, src, filepath.Join(stageDir, name)); err != nil {
			copyErr = fmt.Errorf("copy %s: %w", name, err)
			break
		}
		copied = append(copied, name)
	}
	if copyErr != nil {
		if wasRunning {
			copyErr = errors.Join(copyErr, startService(cfg.OrchestratorService))
		}
		return res, copyErr
	}

	if wasRunning {
		slog.Info("Restarting " + cfg.OrchestratorService + " before archiving sync data…")
		if err := startService(cfg.OrchestratorService); err != nil {
			keepStage = true
			return res, fmt.Errorf("restart %s failed (captured sync data retained at %s): %w", cfg.OrchestratorService, stageDir, err)
		}
	}

	if len(copied) == 0 {
		return res, fmt.Errorf("no sync data directories (%s) found under %s", strings.Join(SyncDirs, ", "), dir)
	}

	archivePath, err := uniqueSyncArchivePath(outDir, nowFn())
	if err != nil {
		keepStage = true
		return res, fmt.Errorf("choose archive path (captured sync data retained at %s): %w", stageDir, err)
	}
	shaHex, size, err := writeSyncArchive(ctx, stageDir, archivePath)
	if err != nil {
		keepStage = true
		return res, fmt.Errorf("write sync backup archive (captured sync data retained at %s): %w", stageDir, err)
	}

	res = SyncBackupResult{
		ArchivePath:  archivePath,
		HashPath:     backup.HashPath(archivePath),
		SHA256:       shaHex,
		IncludedDirs: copied,
		Bytes:        size,
	}
	logx.Success("Orchestrator sync backup written: " + archivePath)
	return res, nil
}

func uniqueSyncArchivePath(dir string, now time.Time) (string, error) {
	base := fmt.Sprintf("orchestrator_sync_%s", now.Format("2006-01-02_150405"))
	for i := 0; i <= 100; i++ {
		name := base + ".tar.gz"
		if i > 0 {
			name = fmt.Sprintf("%s-%d.tar.gz", base, i)
		}
		candidate := filepath.Join(dir, name)
		if _, err := os.Lstat(candidate); errors.Is(err, os.ErrNotExist) {
			return candidate, nil
		} else if err != nil {
			return "", err
		}
	}
	return "", fmt.Errorf("too many sync archives for timestamp %s", base)
}

func writeSyncArchive(ctx context.Context, stageDir, archivePath string) (shaHex string, size int64, err error) {
	partial := archivePath + ".partial"
	f, err := os.OpenFile(partial, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", 0, err
	}
	cleanup := func() {
		_ = os.Remove(partial)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)

	walkErr := filepath.WalkDir(stageDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if path == stageDir {
			return nil
		}
		rel, err := filepath.Rel(stageDir, path)
		if err != nil {
			return err
		}
		relSlash := filepath.ToSlash(rel)
		top := strings.Split(relSlash, "/")[0]
		if !IsSyncDir(top) {
			return fmt.Errorf("refusing non-sync entry %q in sync archive", relSlash)
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink rejected in sync data: %s", relSlash)
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("special file rejected in sync data: %s", relSlash)
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = relSlash
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(tw, in)
		closeErr := in.Close()
		return errors.Join(copyErr, closeErr)
	})
	closeErr := errors.Join(tw.Close(), gz.Close(), f.Close())
	if err := errors.Join(walkErr, closeErr); err != nil {
		cleanup()
		return "", 0, err
	}
	st, err := os.Stat(partial)
	if err != nil {
		cleanup()
		return "", 0, err
	}
	sum, err := backup.SHA256File(partial)
	if err != nil {
		cleanup()
		return "", 0, err
	}
	hashPath := backup.HashPath(archivePath)
	if err := os.WriteFile(hashPath, []byte(sum+"\n"), 0o600); err != nil {
		cleanup()
		return "", 0, err
	}
	if err := os.Rename(partial, archivePath); err != nil {
		cleanup()
		_ = os.Remove(hashPath)
		return "", 0, err
	}
	return sum, st.Size(), nil
}

// copySafeTree copies src into dst while rejecting symlinks and special files.
func copySafeTree(ctx context.Context, src, dst string) error {
	srcInfo, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if srcInfo.Mode()&os.ModeSymlink != 0 || !srcInfo.IsDir() {
		return fmt.Errorf("%s must be a real directory", src)
	}
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return err
	}
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink rejected in sync directory: %s", path)
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm()&0o755|0o700)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("special file rejected in sync directory: %s", path)
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer func() { _ = in.Close() }()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm()&0o600|0o600)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(out, in)
		closeErr := out.Close()
		return errors.Join(copyErr, closeErr)
	})
}

func existingSyncDirs(dir string) ([]string, error) {
	var out []string
	for _, name := range SyncDirs {
		p := filepath.Join(dir, name)
		st, err := os.Lstat(p)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if st.Mode()&os.ModeSymlink != 0 || !st.IsDir() {
			return nil, fmt.Errorf("sync path %s must be a real directory", p)
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

// checkSyncLayout refuses operations if config.json places key material (TSS
// shares or producer key) inside events/ or queues/.
func checkSyncLayout(dir string) error {
	cfgPath := ConfigPath(dir)
	raw, err := ReadConfig(cfgPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil // allow backup even if config needs repair
	}
	var protected []string
	if pRaw, ok := findKeyCI(root, "ProducerKeyFileName"); ok && !isJSONNull(pRaw) {
		var name string
		if err := json.Unmarshal(pRaw, &name); err == nil && strings.TrimSpace(name) != "" {
			protected = append(protected, filepath.Join(dir, strings.TrimLeft(name, "/")))
		}
	}
	if pRaw, ok := findKeyCI(root, "ProducerKeyFilePassphraseFile"); ok && !isJSONNull(pRaw) {
		var path string
		if err := json.Unmarshal(pRaw, &path); err == nil && strings.TrimSpace(path) != "" {
			if !filepath.IsAbs(path) {
				path = filepath.Join(dir, path)
			}
			protected = append(protected, path)
		}
	}
	if tRaw, ok := findKeyCI(root, "TssConfig"); ok && !isJSONNull(tRaw) {
		var tss map[string]json.RawMessage
		if err := json.Unmarshal(tRaw, &tss); err == nil {
			if bRaw, ok := findKeyCI(tss, "BaseDir"); ok && !isJSONNull(bRaw) {
				var base string
				if err := json.Unmarshal(bRaw, &base); err == nil && strings.TrimSpace(base) != "" {
					if !filepath.IsAbs(base) {
						base = filepath.Join(dir, base)
					}
					protected = append(protected, base)
				}
			}
		}
	}
	for _, p := range protected {
		cleanP := filepath.Clean(p)
		for _, sd := range SyncDirs {
			rootDir := filepath.Join(dir, sd)
			if cleanP == rootDir || strings.HasPrefix(cleanP, rootDir+string(filepath.Separator)) {
				return fmt.Errorf("key or TSS path %s overlaps sync directory %s; refusing to copy sync data", cleanP, rootDir)
			}
		}
	}
	return nil
}

// --- Copy sync data to another server ----------------------------------------

var (
	reServiceName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9@._:-]{0,127}$`)
	reUnsafeHost  = regexp.MustCompile(`[\s'"` + "`" + `;&|$<>\\]`)

	// Injectable hooks for tests.
	sshRun     = defaultSSHRun
	rsyncRun   = defaultRsyncRun
	toolExists = execx.Exists
)

// SyncCopyOptions configures CopySyncData.
type SyncCopyOptions struct {
	Host          string
	Port          int
	RemoteDir     string
	RemoteService string
	Sudo          bool
	Progress      io.Writer
	Now           func() time.Time
}

// SyncCopyResult reports what CopySyncData transferred and restarted.
type SyncCopyResult struct {
	Host            string
	RemoteDir       string
	RemoteService   string
	CopiedDirs      []string
	RemoteBackup    string
	LocalRestarted  bool
	RemoteRestarted bool
	RemoteUnitFound bool
}

// Validate checks SSH target, port, remote directory, and remote service name.
func (o *SyncCopyOptions) Validate(cfg config.Config) error {
	o.Host = strings.TrimSpace(o.Host)
	if o.Host == "" {
		return errors.New("destination SSH host is required (for example root@other-server)")
	}
	if strings.HasPrefix(o.Host, "-") || reUnsafeHost.MatchString(o.Host) {
		return fmt.Errorf("invalid SSH destination %q", o.Host)
	}
	for _, r := range o.Host {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("invalid control character in SSH destination %q", o.Host)
		}
	}
	if o.Port == 0 {
		o.Port = 22
	}
	if o.Port < 1 || o.Port > 65535 {
		return fmt.Errorf("SSH port must be between 1 and 65535 (got %d)", o.Port)
	}
	if strings.TrimSpace(o.RemoteDir) == "" {
		o.RemoteDir = cfg.OrchestratorDir
	}
	cleanRemote, err := ValidateDir(o.RemoteDir)
	if err != nil {
		return fmt.Errorf("invalid remote orchestrator directory: %w", err)
	}
	o.RemoteDir = cleanRemote

	if strings.TrimSpace(o.RemoteService) == "" {
		o.RemoteService = cfg.OrchestratorService
	}
	o.RemoteService = strings.TrimSpace(o.RemoteService)
	if !reServiceName.MatchString(o.RemoteService) {
		return fmt.Errorf("invalid remote service name %q", o.RemoteService)
	}
	return nil
}

// CopySyncData stops the local orchestrator, transfers ONLY events/ and
// queues/ over SSH with rsync into a remote staging directory, then stops the
// remote orchestrator (if present), preserves its previous sync directories in
// a timestamped safety backup, atomically moves the staged directories into
// place, and restarts both services that were active.
func CopySyncData(ctx context.Context, cfg config.Config, opts SyncCopyOptions) (SyncCopyResult, error) {
	var res SyncCopyResult
	dir, err := ValidateDir(cfg.OrchestratorDir)
	if err != nil {
		return res, err
	}
	if err := opts.Validate(cfg); err != nil {
		return res, err
	}
	ok, err := Installed(cfg)
	if err != nil {
		return res, err
	}
	if !ok {
		return res, ErrNotInstalled
	}
	if err := checkSyncLayout(dir); err != nil {
		return res, err
	}
	present, err := existingSyncDirs(dir)
	if err != nil {
		return res, err
	}
	if len(present) == 0 {
		return res, fmt.Errorf("no sync data directories (%s) found under %s", strings.Join(SyncDirs, ", "), dir)
	}
	for _, name := range present {
		if err := verifyNoSymlinks(filepath.Join(dir, name)); err != nil {
			return res, err
		}
	}
	for _, tool := range []string{"ssh", "rsync"} {
		if !toolExists(tool) {
			return res, fmt.Errorf("%s is required on this host to copy sync data", tool)
		}
	}

	nowFn := opts.Now
	if nowFn == nil {
		nowFn = time.Now
	}
	stamp := nowFn().UTC().Format("20060102-150405")

	// Inspect the remote host BEFORE stopping the local orchestrator.
	slog.Info("Checking remote host " + opts.Host + "…")
	probeOut, err := sshRun(ctx, opts, remotePreflightScript(opts))
	if err != nil {
		return res, fmt.Errorf("remote preflight on %s failed (ensure SSH key auth and known_hosts are configured): %w", opts.Host, err)
	}
	remoteUnitFound := parseRemoteProbe(probeOut)

	localState, err := serviceStatus(cfg.OrchestratorService)
	if err != nil {
		return res, err
	}
	localWasActive := localState.Running()

	slog.Info("Stopping local " + cfg.OrchestratorService + "…")
	if err := stopService(cfg.OrchestratorService); err != nil {
		if localWasActive {
			err = errors.Join(err, startService(cfg.OrchestratorService))
		}
		return res, err
	}
	localRestarted := false
	restartLocal := func() error {
		if !localWasActive || localRestarted {
			return nil
		}
		slog.Info("Restarting local " + cfg.OrchestratorService + "…")
		if err := startService(cfg.OrchestratorService); err != nil {
			return err
		}
		localRestarted = true
		return nil
	}

	remoteStage := filepath.Join(opts.RemoteDir, ".sync-copy-stage-"+stamp)
	remoteBackup := filepath.Join(opts.RemoteDir, "sync-backup-before-copy-"+stamp)

	if _, err := sshRun(ctx, opts, remoteStageCreateScript(opts, remoteStage)); err != nil {
		return res, errors.Join(fmt.Errorf("prepare remote staging directory: %w", err), restartLocal())
	}

	sources := make([]string, 0, len(present))
	for _, name := range present {
		sources = append(sources, filepath.Join(dir, name))
	}
	slog.Info(fmt.Sprintf("Syncing %s to %s:%s…", strings.Join(present, ", "), opts.Host, opts.RemoteDir))
	if err := rsyncRun(ctx, opts, sources, remoteStage); err != nil {
		_, _ = sshRun(context.Background(), opts, remoteCleanupScript(remoteStage))
		return res, errors.Join(fmt.Errorf("rsync sync data to %s failed: %w", opts.Host, err), restartLocal())
	}

	slog.Info("Installing staged sync data on " + opts.Host + " (keeping safety backup at " + remoteBackup + ")…")
	cutoverOut, err := sshRun(ctx, opts, remoteCutoverScript(opts, remoteStage, remoteBackup, present, remoteUnitFound))
	if err != nil {
		return res, errors.Join(fmt.Errorf("remote cutover on %s failed: %w", opts.Host, err), restartLocal())
	}
	remoteRestarted, err := parseRemoteCutoverRestarted(cutoverOut)
	if err != nil {
		return res, errors.Join(fmt.Errorf("remote cutover on %s did not confirm completion: %w", opts.Host, err), restartLocal())
	}
	remoteRestarted = remoteUnitFound && remoteRestarted

	if err := restartLocal(); err != nil {
		return res, fmt.Errorf("sync data copied to %s, but restarting local %s failed: %w", opts.Host, cfg.OrchestratorService, err)
	}

	res = SyncCopyResult{
		Host:            opts.Host,
		RemoteDir:       opts.RemoteDir,
		RemoteService:   opts.RemoteService,
		CopiedDirs:      present,
		RemoteBackup:    remoteBackup,
		LocalRestarted:  localRestarted,
		RemoteRestarted: remoteRestarted,
		RemoteUnitFound: remoteUnitFound,
	}
	logx.Success(fmt.Sprintf("Copied orchestrator sync data (%s) to %s:%s", strings.Join(present, ", "), opts.Host, opts.RemoteDir))
	return res, nil
}

func verifyNoSymlinks(root string) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink rejected in sync directory: %s", path)
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("special file rejected in sync directory: %s", path)
		}
		return nil
	})
}

func sshQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

func (o SyncCopyOptions) sshArgs() []string {
	return []string{
		"-p", strconv.Itoa(o.Port),
		"-oBatchMode=yes",
		"-oStrictHostKeyChecking=yes",
		"-oConnectTimeout=10",
		"-oServerAliveInterval=5",
		"-oServerAliveCountMax=3",
		"-oForwardAgent=no",
		"-oClearAllForwardings=yes",
	}
}

func (o SyncCopyOptions) wrapRemote(script string) string {
	if o.Sudo {
		return "sudo -n sh -eu -c " + sshQuote(script)
	}
	return "sh -eu -c " + sshQuote(script)
}

func remotePreflightScript(o SyncCopyOptions) string {
	dirQ := sshQuote(o.RemoteDir)
	svcQ := sshQuote(o.RemoteService)
	return fmt.Sprintf(`command -v rsync >/dev/null 2>&1 || { echo "rsync not found on remote host" >&2; exit 1; }
if [ -L %[1]s ]; then echo "remote orchestrator dir is a symlink" >&2; exit 1; fi
if [ -e %[1]s ] && [ ! -d %[1]s ]; then echo "remote orchestrator path is not a directory" >&2; exit 1; fi
if command -v systemctl >/dev/null 2>&1; then
  load=$(systemctl show -p LoadState --value %[2]s 2>/dev/null || echo not-found)
  active=$(systemctl show -p ActiveState --value %[2]s 2>/dev/null || echo inactive)
  echo "LOAD=$load"
  echo "ACTIVE=$active"
else
  echo "LOAD=not-found"
  echo "ACTIVE=inactive"
fi`, dirQ, svcQ)
}

func parseRemoteProbe(out string) (unitFound bool) {
	var load string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if v, ok := strings.CutPrefix(line, "LOAD="); ok {
			load = v
		}
	}
	return load != "" && load != "not-found"
}

func parseRemoteCutoverRestarted(out string) (bool, error) {
	for _, line := range strings.Split(out, "\n") {
		switch strings.TrimSpace(line) {
		case "REMOTE_RESTARTED=1":
			return true, nil
		case "REMOTE_RESTARTED=0":
			return false, nil
		}
	}
	return false, errors.New("missing REMOTE_RESTARTED status line from remote cutover")
}

func remoteStageCreateScript(o SyncCopyOptions, stageDir string) string {
	return fmt.Sprintf(`mkdir -p -m 0700 %[1]s
rm -rf %[2]s
mkdir -m 0700 %[2]s`, sshQuote(o.RemoteDir), sshQuote(stageDir))
}

func remoteCleanupScript(stageDir string) string {
	return fmt.Sprintf(`rm -rf %s`, sshQuote(stageDir))
}

func remoteCutoverScript(o SyncCopyOptions, stageDir, backupDir string, dirs []string, unitFound bool) string {
	dirQ := sshQuote(o.RemoteDir)
	stageQ := sshQuote(stageDir)
	backupQ := sshQuote(backupDir)
	svcQ := sshQuote(o.RemoteService)

	var b strings.Builder
	// Install cleanup/recovery traps before touching any remote state.
	// Signal traps call exit with 128+sig so EXIT runs with a non-zero rc under dash.
	fmt.Fprintf(&b, `restart_remote=0
need_restart=0
swapped=0
backup_created=0
installed_dirs=""
rollback() {
  for d in $installed_dirs; do
    if [ -e %[2]s/"$d" ]; then
      rm -rf %[1]s/"$d"
      mv %[2]s/"$d" %[1]s/"$d"
    else
      rm -rf %[1]s/"$d"
    fi
  done
  for d in %[3]s; do
    if [ -e %[2]s/"$d" ]; then
      rm -rf %[1]s/"$d"
      mv %[2]s/"$d" %[1]s/"$d"
    fi
  done
  rm -rf %[4]s
  if [ "$backup_created" -eq 1 ]; then
    rmdir %[2]s 2>/dev/null || true
  fi
}
`, dirQ, backupQ, strings.Join(dirs, " "), stageQ)

	if unitFound {
		fmt.Fprintf(&b, `on_exit() {
  rc=$?
  trap - EXIT HUP INT TERM
  if [ "$swapped" -eq 1 ]; then
    rollback || true
  elif [ "$rc" -ne 0 ]; then
    rm -rf %[1]s || true
    if [ "$backup_created" -eq 1 ]; then
      rmdir %[2]s 2>/dev/null || true
    fi
  fi
  if [ "$need_restart" -eq 1 ]; then
    systemctl start %[3]s >/dev/null 2>&1 || true
  fi
  exit "$rc"
}
`, stageQ, backupQ, svcQ)
	} else {
		fmt.Fprintf(&b, `on_exit() {
  rc=$?
  trap - EXIT HUP INT TERM
  if [ "$swapped" -eq 1 ]; then
    rollback || true
  elif [ "$rc" -ne 0 ]; then
    rm -rf %[1]s || true
    if [ "$backup_created" -eq 1 ]; then
      rmdir %[2]s 2>/dev/null || true
    fi
  fi
  exit "$rc"
}
`, stageQ, backupQ)
	}

	b.WriteString(`trap on_exit EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM
`)

	// Prepare directories and reject symlink targets BEFORE stopping a running service.
	fmt.Fprintf(&b, `mkdir -p -m 0700 %[1]s
mkdir -m 0700 %[2]s
backup_created=1
for d in %[3]s; do
  if [ -L %[1]s/"$d" ]; then
    echo "remote sync path %[1]s/$d is a symlink" >&2
    exit 1
  fi
done
`, dirQ, backupQ, strings.Join(dirs, " "))

	if unitFound {
		fmt.Fprintf(&b, `active_before=$(systemctl show -p ActiveState --value %[1]s 2>/dev/null || echo inactive)
case "$active_before" in
  active|activating|reloading|refreshing)
    restart_remote=1
    need_restart=1
    ;;
esac
systemctl stop %[1]s
state=$(systemctl show -p ActiveState --value %[1]s)
case "$state" in
  inactive|failed) ;;
  *) echo "remote service %[1]s did not stop (state: $state)" >&2; exit 1 ;;
esac
`, svcQ)
	}

	fmt.Fprintf(&b, `swapped=1
for d in %[3]s; do
  if [ -L %[1]s/"$d" ]; then
    echo "remote sync path %[1]s/$d is a symlink" >&2
    exit 1
  fi
  if [ -e %[1]s/"$d" ]; then
    mv %[1]s/"$d" %[2]s/"$d"
  fi
  mv %[4]s/"$d" %[1]s/"$d"
  installed_dirs="$installed_dirs $d"
done
rm -rf %[4]s
swapped=0
`, dirQ, backupQ, strings.Join(dirs, " "), stageQ)

	if unitFound {
		fmt.Fprintf(&b, `if [ "$restart_remote" -eq 1 ]; then
  systemctl start %[1]s
  need_restart=0
fi
`, svcQ)
	}
	b.WriteString("echo \"REMOTE_RESTARTED=$restart_remote\"\n")
	return b.String()
}

func defaultSSHRun(ctx context.Context, opts SyncCopyOptions, script string) (string, error) {
	args := append(opts.sshArgs(), "--", opts.Host, opts.wrapRemote(script))
	cmd := exec.CommandContext(ctx, "ssh", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg != "" {
			return string(out), fmt.Errorf("%w: %s", err, msg)
		}
		return string(out), err
	}
	return string(out), nil
}

func defaultRsyncRun(ctx context.Context, opts SyncCopyOptions, sources []string, remoteStage string) error {
	sshCmd := append([]string{"ssh"}, opts.sshArgs()...)
	for i, arg := range sshCmd {
		sshCmd[i] = sshQuote(arg)
	}
	args := []string{
		"-aH",
		"--checksum",
		"--numeric-ids",
		"--protect-args",
		"--delete",
		"--info=progress2",
		"-e", strings.Join(sshCmd, " "),
	}
	if opts.Sudo {
		args = append(args, "--rsync-path=sudo -n rsync")
	}
	args = append(args, "--")
	args = append(args, sources...)
	args = append(args, rsyncHost(opts.Host)+":"+remoteStage+"/")
	cmd := exec.CommandContext(ctx, "rsync", args...)
	if opts.Progress != nil {
		cmd.Stdout = opts.Progress
		cmd.Stderr = opts.Progress
	} else {
		cmd.Stdout = io.Discard
		cmd.Stderr = io.Discard
	}
	return cmd.Run()
}

func rsyncHost(host string) string {
	user := ""
	if i := strings.LastIndexByte(host, '@'); i >= 0 {
		user, host = host[:i+1], host[i+1:]
	}
	if ip, err := netip.ParseAddr(host); err == nil && ip.Is6() {
		host = "[" + host + "]"
	}
	return user + host
}
