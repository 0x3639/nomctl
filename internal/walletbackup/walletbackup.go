// Package walletbackup archives the node's wallet directory and config.json,
// the two files that let a node produce and that chain-data backups leave
// out, into a small age-encrypted archive that any machine with the age
// tool can open.
package walletbackup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"filippo.io/age"

	"github.com/0x3639/nomctl/internal/backup"
	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/producer"
)

// Suffixes of an encrypted and a plain archive.
const (
	EncryptedSuffix = ".tar.gz.age"
	PlainSuffix     = ".tar.gz"
)

// MaxArchiveBytes bounds what Restore is willing to decrypt and unpack: a
// wallet directory holds key files of a few hundred bytes each.
const MaxArchiveBytes = 16 << 20

// Dir is where wallet backups live: a subdirectory of the backup directory
// that the chain-data retention rule never touches.
func Dir(cfg config.Config) string { return filepath.Join(cfg.BackupDir, "wallet") }

// Options for Create.
type Options struct {
	// Output directory; Dir(cfg) when empty.
	Output string
	// Passphrase for age; required unless Plain.
	Passphrase string
	// Plain writes an unencrypted archive. The archive holds the producer
	// password, so callers must warn.
	Plain bool
	Now   func() time.Time
}

// Result of Create.
type Result struct {
	Path      string
	HashPath  string
	Files     []string
	Encrypted bool
}

// Create archives wallet/ and config.json from the data directory.
func Create(cfg config.Config, opts Options) (Result, error) {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if !opts.Plain && opts.Passphrase == "" {
		return Result{}, errors.New("a passphrase is required (or --no-encrypt)")
	}
	files, err := collect(cfg.ZnnDir)
	if err != nil {
		return Result{}, err
	}
	if len(files) == 0 {
		return Result{}, fmt.Errorf("nothing to back up: no wallet files or config.json under %s", cfg.ZnnDir)
	}
	archive, err := pack(cfg.ZnnDir, files)
	if err != nil {
		return Result{}, err
	}
	out := opts.Output
	if out == "" {
		out = Dir(cfg)
	}
	if err := os.MkdirAll(out, 0o700); err != nil {
		return Result{}, err
	}
	name := fmt.Sprintf("%s_wallet_%s", cfg.ServiceName, opts.Now().Format("01-02-06_150405"))
	data := archive
	if !opts.Plain {
		if data, err = encrypt(archive, opts.Passphrase); err != nil {
			return Result{}, err
		}
		name += EncryptedSuffix
	} else {
		name += PlainSuffix
	}
	dest := filepath.Join(out, name)
	if err := writeAtomic(dest, data); err != nil {
		return Result{}, err
	}
	sum, err := backup.SHA256File(dest)
	if err != nil {
		return Result{}, err
	}
	hashPath := dest + ".sha256"
	if err := os.WriteFile(hashPath, []byte(sum+"  "+name+"\n"), 0o600); err != nil {
		return Result{}, err
	}
	return Result{Path: dest, HashPath: hashPath, Files: files, Encrypted: !opts.Plain}, nil
}

// collect lists the regular files to archive, relative to znnDir: every
// regular file directly under wallet/, and config.json.
func collect(znnDir string) ([]string, error) {
	var files []string
	entries, err := os.ReadDir(filepath.Join(znnDir, "wallet"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for _, e := range entries {
		if e.Type().IsRegular() {
			files = append(files, "wallet/"+e.Name())
		}
	}
	if st, err := os.Stat(filepath.Join(znnDir, "config.json")); err == nil && st.Mode().IsRegular() {
		files = append(files, "config.json")
	}
	sort.Strings(files)
	return files, nil
}

func pack(znnDir string, files []string) ([]byte, error) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, rel := range files {
		full := filepath.Join(znnDir, filepath.FromSlash(rel))
		data, err := os.ReadFile(full)
		if err != nil {
			return nil, err
		}
		st, err := os.Stat(full)
		if err != nil {
			return nil, err
		}
		h := &tar.Header{Name: rel, Mode: 0o600, Size: int64(len(data)), ModTime: st.ModTime(), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(h); err != nil {
			return nil, err
		}
		if _, err := tw.Write(data); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func encrypt(plain []byte, passphrase string) ([]byte, error) {
	r, err := age.NewScryptRecipient(passphrase)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	w, err := age.Encrypt(&buf, r)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(plain); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ErrWrongPassphrase is returned when an encrypted archive does not open.
var ErrWrongPassphrase = errors.New("wrong passphrase for the wallet backup")

func decrypt(data []byte, passphrase string) ([]byte, error) {
	id, err := age.NewScryptIdentity(passphrase)
	if err != nil {
		return nil, err
	}
	r, err := age.Decrypt(bytes.NewReader(data), id)
	if err != nil {
		var bad *age.NoIdentityMatchError
		if errors.As(err, &bad) {
			return nil, ErrWrongPassphrase
		}
		return nil, err
	}
	out, err := io.ReadAll(io.LimitReader(r, MaxArchiveBytes+1))
	if err != nil {
		return nil, err
	}
	if len(out) > MaxArchiveBytes {
		return nil, errors.New("wallet backup is larger than expected; refusing")
	}
	return out, nil
}

func writeAtomic(dest string, data []byte) error {
	tmp := dest + ".partial"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// Info describes one wallet backup on disk.
type Info struct {
	Path      string
	ModTime   time.Time
	Encrypted bool
}

// List returns the wallet backups in Dir(cfg), newest first. Archives
// without a hash sidecar never finished and are ignored.
func List(cfg config.Config) ([]Info, error) {
	entries, err := os.ReadDir(Dir(cfg))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var infos []Info
	for _, e := range entries {
		name := e.Name()
		enc := strings.HasSuffix(name, EncryptedSuffix)
		if !enc && !strings.HasSuffix(name, PlainSuffix) {
			continue
		}
		p := filepath.Join(Dir(cfg), name)
		if _, err := os.Stat(p + ".sha256"); err != nil {
			continue
		}
		st, err := e.Info()
		if err != nil {
			continue
		}
		infos = append(infos, Info{Path: p, ModTime: st.ModTime(), Encrypted: enc})
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].ModTime.After(infos[j].ModTime) })
	return infos, nil
}

// Contents is what an archive holds, after Inspect.
type Contents struct {
	Files []string
	// Producer is the Producer section of the archived config.json, when
	// present.
	Producer *producer.Config
	// KeyFile is the archived key file named by Producer, when both exist.
	KeyFile string
}

// Open reads an archive (decrypting when its name ends in .age), verifies
// the sidecar when present, and returns the archive bytes.
func Open(path, passphrase string) ([]byte, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if st.Size() > MaxArchiveBytes {
		return nil, errors.New("wallet backup is larger than expected; refusing")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if sidecar, err := os.ReadFile(path + ".sha256"); err == nil {
		want := strings.Fields(string(sidecar))
		got, err := backup.SHA256File(path)
		if err != nil {
			return nil, err
		}
		if len(want) == 0 || !strings.EqualFold(want[0], got) {
			return nil, errors.New("wallet backup does not match its .sha256 sidecar")
		}
	}
	if strings.HasSuffix(path, ".age") {
		if passphrase == "" {
			return nil, errors.New("this backup is encrypted; a passphrase is required")
		}
		return decrypt(data, passphrase)
	}
	return data, nil
}

// Inspect validates the archive layout: only regular files named
// wallet/<name> or config.json. It reports what is inside.
func Inspect(archive []byte) (Contents, error) {
	var c Contents
	entries := map[string][]byte{}
	b := newBudget()
	if err := walk(archive, func(h *tar.Header, r io.Reader) error {
		if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeRegA { //nolint:staticcheck // older writers
			return fmt.Errorf("wallet backup entry %s is not a regular file; refusing", h.Name)
		}
		rel, err := entryPath(h.Name)
		if err != nil {
			return err
		}
		var buf bytes.Buffer
		if err := b.copy(&buf, r); err != nil {
			return err
		}
		data := buf.Bytes()
		entries[rel] = data
		c.Files = append(c.Files, rel)
		return nil
	}); err != nil {
		return Contents{}, err
	}
	sort.Strings(c.Files)
	if len(c.Files) == 0 {
		return Contents{}, errors.New("wallet backup is empty")
	}
	if cfgData, ok := entries["config.json"]; ok {
		tmp, err := os.CreateTemp("", "nomctl-wallet-config-")
		if err != nil {
			return Contents{}, err
		}
		defer func() { _ = os.Remove(tmp.Name()) }()
		if _, err := tmp.Write(cfgData); err != nil {
			_ = tmp.Close()
			return Contents{}, err
		}
		_ = tmp.Close()
		pc, err := producer.ReadConfig(tmp.Name())
		if err != nil {
			return Contents{}, fmt.Errorf("archived config.json: %w", err)
		}
		c.Producer = pc
		if pc != nil && pc.KeyFilePath != "" {
			if _, ok := entries["wallet/"+path.Base(pc.KeyFilePath)]; ok {
				c.KeyFile = "wallet/" + path.Base(pc.KeyFilePath)
			}
		}
	}
	return c, nil
}

// entryPath accepts "wallet/<name>" and "config.json" (with or without a
// leading "./") and nothing else.
func entryPath(name string) (string, error) {
	clean := path.Clean("/" + name)
	clean = strings.TrimPrefix(clean, "/")
	if clean == "config.json" {
		return clean, nil
	}
	if dir, base := path.Split(clean); dir == "wallet/" && base != "" && base != "." && base != ".." {
		return clean, nil
	}
	return "", fmt.Errorf("wallet backup entry %s is outside wallet/ and config.json; refusing", name)
}

// budget bounds the total bytes unpacked from one archive across entries.
type budget struct{ left int64 }

func newBudget() *budget { return &budget{left: MaxArchiveBytes} }

// copy moves r to w within the remaining budget and fails, rather than
// truncating, when an entry would exceed it.
func (b *budget) copy(w io.Writer, r io.Reader) error {
	n, err := io.Copy(w, io.LimitReader(r, b.left+1))
	if err != nil {
		return err
	}
	if n > b.left {
		return fmt.Errorf("wallet backup exceeds %d bytes; refusing", MaxArchiveBytes)
	}
	b.left -= n
	return nil
}

func walk(archive []byte, fn func(*tar.Header, io.Reader) error) error {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return fmt.Errorf("wallet backup is not a gzip archive: %w", err)
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read wallet backup: %w", err)
		}
		if err := fn(h, tr); err != nil {
			return err
		}
	}
}

// RestoreResult describes what Restore did.
type RestoreResult struct {
	Files      []string
	SafetyDir  string
	Address    string // producer address in the restored key, when any
	Restarted  bool
	WasRunning bool
}

// Hooks so tests can stub the host.
var (
	serviceRunning = func(string) (bool, error) { return false, nil }
	stop           = func(string) error { return nil }
	restart        = func(string) error { return nil }
	// Rename hooks inject installation and recovery failures in tests.
	installRename  = os.Rename
	rollbackRename = os.Rename
)

// SetServiceHooks wires the service functions (the cmd package does).
func SetServiceHooks(running func(string) (bool, error), stopFn, restartFn func(string) error) {
	serviceRunning, stop, restart = running, stopFn, restartFn
}

// Restore installs an archive's files into the data directory. The whole
// current wallet directory and config.json are moved into a unique private
// .wallet-safety.<unix>-* directory under the node data directory, so the
// safety moves stay on the same filesystem. When the archive's config names
// a producer key, that key must be present, open with the archived password
// and derive the archived address at Producer.Index, or nothing is touched.
// A running node requires restartNode. The service is stopped before any
// live files move. A failure after the move puts the previous files back,
// including the absence of paths that did not originally exist, and leaves
// the node stopped. If a failed restart cannot be stopped, the installed
// files are left intact and the safety files are preserved for recovery.
func Restore(cfg config.Config, archive []byte, restartNode bool, now time.Time) (RestoreResult, error) {
	c, err := Inspect(archive)
	if err != nil {
		return RestoreResult{}, err
	}
	if err := os.MkdirAll(cfg.ZnnDir, 0o700); err != nil {
		return RestoreResult{}, err
	}
	staging, err := os.MkdirTemp(cfg.ZnnDir, ".wallet-restore-")
	if err != nil {
		return RestoreResult{}, err
	}
	defer func() { _ = os.RemoveAll(staging) }()
	b := newBudget()
	if err := walk(archive, func(h *tar.Header, r io.Reader) error {
		rel, err := entryPath(h.Name)
		if err != nil {
			return err
		}
		target := filepath.Join(staging, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		if err := b.copy(out, r); err != nil {
			_ = out.Close()
			return err
		}
		return out.Close()
	}); err != nil {
		return RestoreResult{}, err
	}
	res := RestoreResult{Files: c.Files}
	if c.Producer != nil {
		if c.KeyFile == "" {
			return res, fmt.Errorf("the archived config.json names key file %q but the archive does not contain it", c.Producer.KeyFilePath)
		}
		addr, err := producer.VerifyAtIndex(filepath.Join(staging, filepath.FromSlash(c.KeyFile)), c.Producer.Password, c.Producer.Index)
		if err != nil {
			return res, fmt.Errorf("verify archived producer key: %w", err)
		}
		if !strings.EqualFold(addr, c.Producer.Address) {
			return res, fmt.Errorf("archived key file derives %s at index %d but the archived config expects %s", addr, c.Producer.Index, c.Producer.Address)
		}
		res.Address = addr
	}

	res.WasRunning, err = serviceRunning(cfg.ServiceName)
	if err != nil {
		return res, fmt.Errorf("cannot determine node state before wallet restore: %w", err)
	}
	if res.WasRunning && !restartNode {
		return res, errors.New("the node is running; stop it first or pass --restart to restore wallet files")
	}

	// Staging and safety both live on the data filesystem. A separate backup
	// disk must not turn the safety rename into a cross-device operation.
	safety, err := os.MkdirTemp(cfg.ZnnDir, fmt.Sprintf(".wallet-safety.%d-", now.Unix()))
	if err != nil {
		return res, err
	}
	// Even a service that was inactive may be transitioning. Stop must
	// establish that it is stopped before any live path is changed.
	if err := stop(cfg.ServiceName); err != nil {
		_ = os.Remove(safety)
		return res, fmt.Errorf("stop node before wallet restore: %w", err)
	}
	res.SafetyDir = safety
	type original struct {
		live, saved string
		existed     bool
	}
	var originals []original
	rollback := func(cause error) error {
		var errs []error
		for i := len(originals) - 1; i >= 0; i-- {
			o := originals[i]
			if err := os.RemoveAll(o.live); err != nil {
				errs = append(errs, fmt.Errorf("remove replacement %s: %w", filepath.Base(o.live), err))
				continue
			}
			if o.existed {
				if err := rollbackRename(o.saved, o.live); err != nil {
					errs = append(errs, fmt.Errorf("put back %s: %w", filepath.Base(o.live), err))
				}
			}
		}
		if len(errs) > 0 {
			return fmt.Errorf("%w; previous files could not all be put back; node left stopped; inspect the data directory and %s before starting: %w", cause, safety, errors.Join(errs...))
		}
		return fmt.Errorf("%w (previous files put back; node left stopped)", cause)
	}
	for _, name := range []string{"wallet", "config.json"} {
		o := original{live: filepath.Join(cfg.ZnnDir, name), saved: filepath.Join(safety, name)}
		if _, err := os.Lstat(o.live); err == nil {
			if err := os.Rename(o.live, o.saved); err != nil {
				return res, rollback(fmt.Errorf("move %s aside: %w", name, err))
			}
			o.existed = true
		} else if !errors.Is(err, os.ErrNotExist) {
			return res, rollback(fmt.Errorf("inspect current %s: %w", name, err))
		}
		// Record absent paths as well, so rollback removes newly installed
		// files and directories that have no previous version to put back.
		originals = append(originals, o)
	}
	for _, rel := range c.Files {
		src := filepath.Join(staging, filepath.FromSlash(rel))
		dst := filepath.Join(cfg.ZnnDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return res, rollback(err)
		}
		if err := installRename(src, dst); err != nil {
			return res, rollback(fmt.Errorf("install %s: %w", rel, err))
		}
	}
	if restartNode && res.WasRunning {
		if err := restart(cfg.ServiceName); err != nil {
			// A failed start may have spawned a process. Never replace its
			// files until a subsequent stop confirms it cannot use them.
			if stopErr := stop(cfg.ServiceName); stopErr != nil {
				return res, fmt.Errorf("restart restored node: %w; cannot confirm node stopped: %w; restored files remain installed and previous files are preserved in %s; stop the node before recovery", err, stopErr, safety)
			}
			return res, rollback(fmt.Errorf("restart restored node: %w", err))
		}
		res.Restarted = true
	}
	return res, nil
}

// Status is what the wallet_backup_missing alert needs.
type Status struct {
	ProducerConfigured bool
	KeyFileModTime     time.Time
	NewestBackup       time.Time
}

// Check reports whether a producer is configured and when its key and the
// newest wallet backup were written.
func Check(cfg config.Config) Status {
	var s Status
	pc, err := producer.ReadConfig(producer.ConfigPath(cfg.ZnnDir))
	if err != nil || pc == nil {
		return s
	}
	s.ProducerConfigured = true
	if st, err := os.Stat(producer.KeyFilePath(cfg.ZnnDir)); err == nil {
		s.KeyFileModTime = st.ModTime()
	}
	if infos, err := List(cfg); err == nil && len(infos) > 0 {
		s.NewestBackup = infos[0].ModTime
	}
	return s
}

// Missing reports whether a configured producer lacks a backup made after
// its key file was written.
func (s Status) Missing() bool {
	return s.ProducerConfigured && (s.NewestBackup.IsZero() || s.NewestBackup.Before(s.KeyFileModTime))
}
