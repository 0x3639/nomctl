package orchestrator

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
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
	return checkSyncLayoutBytes(dir, raw, SyncDirs, false)
}

func checkSyncLayoutBytes(dir string, raw []byte, dirs []string, strictJSON bool) error {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		if strictJSON {
			return fmt.Errorf("cannot parse orchestrator config.json under %s to verify protected key paths: %w", dir, err)
		}
		return nil // allow local backup even if config needs repair
	}
	baseDirs := []string{dir}
	if dRaw, ok := findKeyCI(root, "DataPath"); ok && !isJSONNull(dRaw) {
		var dataPath string
		if err := json.Unmarshal(dRaw, &dataPath); err == nil {
			dataPath = strings.TrimSpace(dataPath)
			if filepath.IsAbs(dataPath) {
				cleanData := filepath.Clean(dataPath)
				if cleanData != dir {
					baseDirs = append(baseDirs, cleanData)
				}
			}
		}
	}
	var protected []string
	addProtected := func(rawPath string, joinAbsolute bool) {
		rawPath = strings.TrimSpace(rawPath)
		if rawPath == "" {
			return
		}
		if filepath.IsAbs(rawPath) {
			protected = append(protected, rawPath)
			if joinAbsolute {
				for _, base := range baseDirs {
					protected = append(protected, filepath.Join(base, strings.TrimLeft(rawPath, "/")))
				}
			}
			return
		}
		for _, base := range baseDirs {
			protected = append(protected, filepath.Join(base, rawPath))
		}
	}
	if pRaw, ok := findKeyCI(root, "ProducerKeyFileName"); ok && !isJSONNull(pRaw) {
		var name string
		if err := json.Unmarshal(pRaw, &name); err == nil {
			addProtected(name, true)
		}
	}
	if pRaw, ok := findKeyCI(root, "ProducerKeyFilePassphraseFile"); ok && !isJSONNull(pRaw) {
		var path string
		if err := json.Unmarshal(pRaw, &path); err == nil {
			addProtected(path, false)
		}
	}
	if tRaw, ok := findKeyCI(root, "TssConfig"); ok && !isJSONNull(tRaw) {
		var tss map[string]json.RawMessage
		if err := json.Unmarshal(tRaw, &tss); err == nil {
			if bRaw, ok := findKeyCI(tss, "BaseDir"); ok && !isJSONNull(bRaw) {
				var base string
				if err := json.Unmarshal(bRaw, &base); err == nil {
					addProtected(base, false)
				}
			}
		}
	}
	for _, p := range protected {
		cleanP := filepath.Clean(p)
		for _, sd := range dirs {
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
	reServiceName    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9@._:-]{0,127}$`)
	reUnsafeHost     = regexp.MustCompile(`[\s'"` + "`" + `;&|$<>\\]`)
	errSSHNotStarted = errors.New("ssh command did not start")

	// Injectable hooks for tests.
	sshRun                       = defaultSSHRun
	rsyncRun                     = defaultRsyncRun
	toolExists                   = execx.Exists
	copyNonce                    = defaultCopyNonce
	remoteCutoverLockWaitSeconds = 120
	remoteCleanupTimeout         = 15 * time.Second
	sshWaitDelay                 = 2 * time.Second
)

func defaultCopyNonce() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

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
	nonce, err := copyNonce()
	if err != nil {
		return res, fmt.Errorf("generate sync copy token: %w", err)
	}
	token := stamp + "-" + nonce

	// Inspect the remote host and validate destination layout BEFORE stopping
	// the local orchestrator.
	slog.Info("Checking remote host " + opts.Host + "…")
	probeOut, err := sshRun(ctx, opts, remotePreflightScript(opts))
	if err != nil {
		return res, fmt.Errorf("remote preflight on %s failed (ensure SSH key auth and known_hosts are configured): %w", opts.Host, err)
	}
	probe, err := parseRemoteProbe(probeOut)
	if err != nil {
		return res, fmt.Errorf("remote preflight on %s returned incomplete state: %w", opts.Host, err)
	}
	if probe.configPresent {
		if err := checkSyncLayoutBytes(opts.RemoteDir, probe.configRaw, present, true); err != nil {
			return res, fmt.Errorf("remote destination %s:%s failed layout check: %w", opts.Host, opts.RemoteDir, err)
		}
	}

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

	remoteStage := filepath.Join(opts.RemoteDir, ".sync-copy-stage-"+token)
	remoteBackup := filepath.Join(opts.RemoteDir, "sync-backup-before-copy-"+token)
	cleanupRemoteStage := func() {
		cleanCtx, cancel := context.WithTimeout(context.Background(), remoteCleanupTimeout)
		defer cancel()
		_, _ = sshRun(cleanCtx, opts, remoteCleanupScript(remoteStage))
	}

	if _, err := sshRun(ctx, opts, remoteStageCreateScript(opts, remoteStage)); err != nil {
		return res, errors.Join(fmt.Errorf("prepare remote staging directory: %w", err), restartLocal())
	}

	sources := make([]string, 0, len(present))
	for _, name := range present {
		sources = append(sources, filepath.Join(dir, name))
	}
	slog.Info(fmt.Sprintf("Syncing %s to %s:%s…", strings.Join(present, ", "), opts.Host, opts.RemoteDir))
	if err := rsyncRun(ctx, opts, sources, remoteStage); err != nil {
		restartErr := restartLocal()
		cleanupRemoteStage()
		return res, errors.Join(fmt.Errorf("rsync sync data to %s failed: %w", opts.Host, err), restartErr)
	}

	if err := ctx.Err(); err != nil {
		restartErr := restartLocal()
		cleanupRemoteStage()
		return res, errors.Join(fmt.Errorf("sync copy to %s cancelled before remote cutover: %w", opts.Host, err), restartErr)
	}

	slog.Info("Installing staged sync data on " + opts.Host + " (keeping safety backup at " + remoteBackup + ")…")
	cutoverOut, err := sshRun(ctx, opts, remoteCutoverScript(opts, remoteStage, remoteBackup, present, probe.configSHA256))
	if err != nil {
		restartErr := restartLocal()
		if errors.Is(err, errSSHNotStarted) {
			cleanupRemoteStage()
		}
		return res, errors.Join(fmt.Errorf("remote cutover on %s failed: %w", opts.Host, err), restartErr)
	}
	remoteUnitFound, remoteRestarted, err := parseRemoteCutoverResult(cutoverOut)
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
	cfgQ := sshQuote(ConfigPath(o.RemoteDir))
	return fmt.Sprintf(`command -v rsync >/dev/null 2>&1 || { echo "rsync not found on remote host" >&2; exit 1; }
if [ -L %[1]s ]; then echo "remote orchestrator dir is a symlink" >&2; exit 1; fi
if [ -e %[1]s ] && [ ! -d %[1]s ]; then echo "remote orchestrator path is not a directory" >&2; exit 1; fi
if [ -L %[3]s ]; then echo "remote orchestrator config is a symlink" >&2; exit 1; fi
if command -v systemctl >/dev/null 2>&1; then
  load=$(systemctl show -p LoadState --value %[2]s)
  active=$(systemctl show -p ActiveState --value %[2]s)
  if [ -z "$load" ] || [ -z "$active" ]; then
    echo "cannot determine state of remote service %[2]s" >&2
    exit 1
  fi
  echo "LOAD=$load"
  echo "ACTIVE=$active"
else
  echo "LOAD=not-found"
  echo "ACTIVE=inactive"
fi
if [ -e %[3]s ]; then
  if [ ! -f %[3]s ]; then echo "remote orchestrator config is not a regular file" >&2; exit 1; fi
  cfg_size=$(wc -c < %[3]s | tr -d '[:space:]')
  if [ "$cfg_size" -gt %[4]d ]; then echo "remote orchestrator config exceeds maximum size" >&2; exit 1; fi
  if command -v sha256sum >/dev/null 2>&1; then
    cfg_sha=$(sha256sum %[3]s | awk '{print $1}')
  elif command -v shasum >/dev/null 2>&1; then
    cfg_sha=$(shasum -a 256 %[3]s | awk '{print $1}')
  else
    echo "sha256sum or shasum is required on remote host" >&2
    exit 1
  fi
  echo "CONFIG_PRESENT=1"
  echo "CONFIG_SHA256=$cfg_sha"
  echo "CONFIG_B64_BEGIN"
  base64 < %[3]s | tr -d '\r\n'
  echo ""
  echo "CONFIG_B64_END"
else
  echo "CONFIG_PRESENT=0"
fi`, dirQ, svcQ, cfgQ, MaxConfigBytes)
}

type remoteProbe struct {
	configPresent bool
	configSHA256  string
	configRaw     []byte
}

func parseRemoteProbe(out string) (remoteProbe, error) {
	var p remoteProbe
	seenPresent := false
	inB64 := false
	seenB64End := false
	var b64Buf strings.Builder

	for _, rawLine := range strings.Split(out, "\n") {
		line := strings.TrimSpace(rawLine)
		if inB64 {
			if line == "CONFIG_B64_END" {
				inB64 = false
				seenB64End = true
				continue
			}
			b64Buf.WriteString(line)
			continue
		}
		switch {
		case line == "CONFIG_PRESENT=0":
			seenPresent = true
			p.configPresent = false
		case line == "CONFIG_PRESENT=1":
			seenPresent = true
			p.configPresent = true
		case strings.HasPrefix(line, "CONFIG_SHA256="):
			p.configSHA256 = strings.ToLower(strings.TrimPrefix(line, "CONFIG_SHA256="))
		case line == "CONFIG_B64_BEGIN":
			inB64 = true
		}
	}
	if !seenPresent {
		return p, errors.New("missing CONFIG_PRESENT status from remote preflight")
	}
	if !p.configPresent {
		p.configSHA256 = ""
		return p, nil
	}
	if len(p.configSHA256) != 64 || !seenB64End {
		return p, errors.New("incomplete remote config.json payload from preflight")
	}
	raw, err := base64.StdEncoding.DecodeString(b64Buf.String())
	if err != nil {
		return p, fmt.Errorf("decode remote config.json: %w", err)
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != p.configSHA256 {
		return p, errors.New("remote config.json checksum mismatch during preflight")
	}
	p.configRaw = raw
	return p, nil
}

func parseRemoteCutoverResult(out string) (unitFound, restarted bool, err error) {
	seenUnit := false
	seenRestarted := false
	for _, line := range strings.Split(out, "\n") {
		switch strings.TrimSpace(line) {
		case "REMOTE_UNIT_FOUND=1":
			unitFound = true
			seenUnit = true
		case "REMOTE_UNIT_FOUND=0":
			unitFound = false
			seenUnit = true
		case "REMOTE_RESTARTED=1":
			restarted = true
			seenRestarted = true
		case "REMOTE_RESTARTED=0":
			restarted = false
			seenRestarted = true
		}
	}
	if !seenUnit || !seenRestarted {
		return false, false, errors.New("missing REMOTE_UNIT_FOUND/REMOTE_RESTARTED status line from remote cutover")
	}
	return unitFound, restarted, nil
}

func remoteStageCreateScript(o SyncCopyOptions, stageDir string) string {
	return fmt.Sprintf(`umask 077
if [ -L %[1]s ]; then echo "remote orchestrator dir is a symlink" >&2; exit 1; fi
if [ -e %[1]s ] && [ ! -d %[1]s ]; then echo "remote orchestrator path is not a directory" >&2; exit 1; fi
mkdir -p -m 0700 %[1]s
mkdir -m 0700 %[2]s`, sshQuote(o.RemoteDir), sshQuote(stageDir))
}

func remoteCleanupScript(stageDir string) string {
	return fmt.Sprintf(`rm -rf %s`, sshQuote(stageDir))
}

func remoteCutoverScript(o SyncCopyOptions, stageDir, backupDir string, dirs []string, expectedConfigSHA string) string {
	dirQ := sshQuote(o.RemoteDir)
	stageQ := sshQuote(stageDir)
	backupQ := sshQuote(backupDir)
	svcQ := sshQuote(o.RemoteService)
	cfgQ := sshQuote(ConfigPath(o.RemoteDir))
	lockFileQ := sshQuote(filepath.Join(o.RemoteDir, ".nomctl-sync-cutover.lock"))
	lockDirQ := sshQuote(filepath.Join(o.RemoteDir, ".nomctl-sync-cutover.lockdir"))
	dirsList := strings.Join(dirs, " ")

	var b strings.Builder
	// Install cleanup/recovery traps before touching any remote state.
	// Ignore SIGPIPE so a dropped SSH client (broken pipe on stdout/stderr)
	// fails writes with EPIPE -> errexit -> on_exit instead of killing the shell.
	// Signal traps call exit with 128+sig so EXIT runs with a non-zero rc under dash.
	fmt.Fprintf(&b, `umask 077
trap '' PIPE
unit_found=0
restart_remote=0
need_restart=0
swapped=0
start_attempted=0
backup_created=0
installed_dirs=""
lock_dir_held=0

stop_remote_unit() {
  systemctl stop %[5]s || return 1
  state=$(systemctl show -p ActiveState --value %[5]s) || return 1
  case "$state" in
    inactive|failed) return 0 ;;
    *)
      echo "remote service %[5]s did not stop (state: $state)" >&2
      return 1
      ;;
  esac
}

rollback() {
  rb_ok=1
  for d in %[3]s; do
    if [ -e %[2]s/"$d" ]; then
      if ! rm -rf %[1]s/"$d"; then
        echo "rollback failed: could not remove %[1]s/$d (backup preserved at %[2]s)" >&2
        rb_ok=0
        continue
      fi
      if ! mv %[2]s/"$d" %[1]s/"$d"; then
        echo "rollback failed: could not restore %[2]s/$d to %[1]s/$d (backup preserved at %[2]s)" >&2
        rb_ok=0
      fi
    else
      case " $installed_dirs " in
        *" $d "*)
          if ! rm -rf %[1]s/"$d"; then
            echo "rollback failed: could not remove newly installed %[1]s/$d" >&2
            rb_ok=0
          fi
          ;;
      esac
    fi
  done
  if [ "$rb_ok" -eq 1 ]; then
    rm -rf %[4]s || true
    if [ "$backup_created" -eq 1 ]; then
      rmdir %[2]s 2>/dev/null || true
    fi
    return 0
  fi
  echo "rollback incomplete; previous sync data preserved at %[2]s (stage at %[4]s)" >&2
  return 1
}

on_exit() {
  rc=$?
  set +e
  trap '' HUP INT TERM
  trap - EXIT
  # Flush any buffered stdout left behind after EPIPE on bash-based /bin/sh
  # so subsequent $(...) command substitutions inside recovery are not polluted.
  echo >/dev/null
  allow_restart=1
  if [ "$swapped" -eq 1 ]; then
    if [ "$start_attempted" -eq 1 ] && [ "$unit_found" -eq 1 ]; then
      if ! stop_remote_unit; then
        echo "rollback aborted: remote service %[5]s could not be confirmed stopped after startup failure; previous sync data preserved at %[2]s" >&2
        allow_restart=0
      elif ! rollback; then
        allow_restart=0
      fi
    else
      if ! rollback; then
        allow_restart=0
      fi
    fi
  elif [ "$rc" -ne 0 ]; then
    rm -rf %[4]s || true
    if [ "$backup_created" -eq 1 ]; then
      rmdir %[2]s 2>/dev/null || true
    fi
  fi
  if [ "$allow_restart" -eq 1 ] && [ "$unit_found" -eq 1 ] && [ "$need_restart" -eq 1 ]; then
    if ! systemctl start %[5]s; then
      echo "failed to restart remote service %[5]s during recovery" >&2
    fi
  fi
  if [ "$lock_dir_held" -eq 1 ]; then
    rmdir %[6]s 2>/dev/null || true
  fi
  exit "$rc"
}

trap on_exit EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM
`, dirQ, backupQ, dirsList, stageQ, svcQ, lockDirQ)

	// Serialize cutover per destination before validating config, stopping service, or swapping dirs.
	fmt.Fprintf(&b, `if [ -L %[1]s ]; then
  echo "remote orchestrator dir is a symlink" >&2
  exit 1
fi
if [ -e %[1]s ] && [ ! -d %[1]s ]; then
  echo "remote orchestrator path is not a directory" >&2
  exit 1
fi
mkdir -p -m 0700 %[1]s
if command -v flock >/dev/null 2>&1; then
  if [ -L %[2]s ]; then
    echo "remote cutover lock %[2]s is a symlink" >&2
    exit 1
  fi
  exec 9>>%[2]s
  if ! flock -w %[4]d 9; then
    echo "timed out waiting for remote cutover lock %[2]s" >&2
    exit 1
  fi
else
  lock_waited=0
  while ! mkdir %[3]s 2>/dev/null; do
    if [ "$lock_waited" -ge %[4]d ]; then
      echo "timed out waiting for remote cutover lock %[3]s (remove %[3]s if no other copy-sync is running)" >&2
      exit 1
    fi
    sleep 1
    lock_waited=$((lock_waited + 1))
  done
  lock_dir_held=1
fi
`, dirQ, lockFileQ, lockDirQ, remoteCutoverLockWaitSeconds)

	// Ensure destination config.json matches the layout validated before cutover.
	if expectedConfigSHA == "" {
		fmt.Fprintf(&b, `if [ -L %[1]s ] || [ -e %[1]s ]; then
  echo "remote orchestrator config appeared during sync copy; retry" >&2
  exit 1
fi
`, cfgQ)
	} else {
		fmt.Fprintf(&b, `if [ -L %[1]s ] || [ ! -f %[1]s ]; then
  echo "remote orchestrator config changed during sync copy; retry" >&2
  exit 1
fi
if command -v sha256sum >/dev/null 2>&1; then
  cur_cfg_sha=$(sha256sum %[1]s | awk '{print $1}' | tr 'A-F' 'a-f')
elif command -v shasum >/dev/null 2>&1; then
  cur_cfg_sha=$(shasum -a 256 %[1]s | awk '{print $1}' | tr 'A-F' 'a-f')
else
  echo "sha256sum or shasum is required on remote host" >&2
  exit 1
fi
if [ "$cur_cfg_sha" != %[2]s ]; then
  echo "remote orchestrator config changed during sync copy; retry" >&2
  exit 1
fi
`, cfgQ, sshQuote(expectedConfigSHA))
	}

	// Prepare backup dir and reject symlink targets BEFORE stopping a running service.
	fmt.Fprintf(&b, `mkdir -m 0700 %[2]s
backup_created=1
for d in %[3]s; do
  if [ -L %[1]s/"$d" ]; then
    echo "remote sync path %[1]s/$d is a symlink" >&2
    exit 1
  fi
done
`, dirQ, backupQ, dirsList)

	// Check live remote unit existence and active state at cutover time; fail closed on query errors.
	fmt.Fprintf(&b, `if command -v systemctl >/dev/null 2>&1; then
  load_state=$(systemctl show -p LoadState --value %[1]s)
  active_before=$(systemctl show -p ActiveState --value %[1]s)
  if [ -z "$load_state" ] || [ -z "$active_before" ]; then
    echo "cannot determine state of remote service %[1]s" >&2
    exit 1
  fi
  if [ "$load_state" != "not-found" ] || [ "$active_before" != "inactive" ]; then
    unit_found=1
  fi
fi
if [ "$unit_found" -eq 1 ]; then
  case "$active_before" in
    active|activating|reloading|refreshing)
      restart_remote=1
      need_restart=1
      ;;
    inactive|failed|deactivating|maintenance)
      ;;
    *)
      echo "unexpected ActiveState for remote service %[1]s: $active_before" >&2
      exit 1
      ;;
  esac
  stop_remote_unit
fi
`, svcQ)

	// Swap staged sync directories into place; record each directory before moving so signal rollback covers it.
	fmt.Fprintf(&b, `swapped=1
for d in %[3]s; do
  if [ -L %[1]s/"$d" ]; then
    echo "remote sync path %[1]s/$d is a symlink" >&2
    exit 1
  fi
  if [ -e %[1]s/"$d" ]; then
    mv %[1]s/"$d" %[2]s/"$d"
  fi
  installed_dirs="$installed_dirs $d"
  mv %[4]s/"$d" %[1]s/"$d"
done
`, dirQ, backupQ, dirsList, stageQ)

	// Keep rollback armed until remote startup succeeds (or commit immediately if unit stays stopped).
	fmt.Fprintf(&b, `if [ "$unit_found" -eq 1 ] && [ "$restart_remote" -eq 1 ]; then
  start_attempted=1
  systemctl start %[1]s
  post_state=$(systemctl show -p ActiveState --value %[1]s)
  case "$post_state" in
    active) ;;
    *)
      echo "remote service %[1]s is $post_state after restart" >&2
      exit 1
      ;;
  esac
  swapped=0
  need_restart=0
  backup_created=0
  rm -rf %[2]s || true
else
  swapped=0
  backup_created=0
  rm -rf %[2]s || true
fi
echo "REMOTE_UNIT_FOUND=$unit_found"
echo "REMOTE_RESTARTED=$restart_remote"
`, svcQ, stageQ)

	return b.String()
}

func defaultSSHRun(ctx context.Context, opts SyncCopyOptions, script string) (string, error) {
	args := append(opts.sshArgs(), "--", opts.Host, opts.wrapRemote(script))
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.WaitDelay = sshWaitDelay
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("%w: %w", errSSHNotStarted, err)
	}
	if err := cmd.Wait(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return stdout.String(), fmt.Errorf("%w: %s", err, msg)
		}
		return stdout.String(), err
	}
	return stdout.String(), nil
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
