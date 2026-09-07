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
	if err := walk(archive, func(h *tar.Header, r io.Reader) error {
		if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeRegA { //nolint:staticcheck // older writers
			return fmt.Errorf("wallet backup entry %s is not a regular file; refusing", h.Name)
		}
		rel, err := entryPath(h.Name)
		if err != nil {
			return err
		}
		data, err := io.ReadAll(io.LimitReader(r, MaxArchiveBytes))
		if err != nil {
			return err
		}
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
	isActive = func(string) bool { return false }
	restart  = func(string) error { return nil }
)

// SetServiceHooks wires the service functions (the cmd package does).
func SetServiceHooks(active func(string) bool, restartFn func(string) error) {
	isActive, restart = active, restartFn
}

// Restore installs an archive's files into the data directory. The current
// wallet directory and config.json are moved into
// <backup dir>/restore/wallet-safety.<unix>/ first. When the archive's
// config names a producer key, that key must be present and its address
// must match, or nothing is touched. With restartNode the node is
// restarted so it loads the files.
func Restore(cfg config.Config, archive []byte, restartNode bool, now time.Time) (RestoreResult, error) {
	c, err := Inspect(archive)
	if err != nil {
		return RestoreResult{}, err
	}
	staging, err := os.MkdirTemp(cfg.ZnnDir, ".wallet-restore-")
	if err != nil {
		return RestoreResult{}, err
	}
	defer func() { _ = os.RemoveAll(staging) }()
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
		if _, err := io.Copy(out, io.LimitReader(r, MaxArchiveBytes)); err != nil {
			_ = out.Close()
			return err
		}
		return out.Close()
	}); err != nil {
		return RestoreResult{}, err
	}
	res := RestoreResult{Files: c.Files, WasRunning: isActive(cfg.ServiceName)}
	if c.Producer != nil {
		if c.KeyFile == "" {
			return res, fmt.Errorf("the archived config.json names key file %q but the archive does not contain it", c.Producer.KeyFilePath)
		}
		addr, err := producer.Verify(filepath.Join(staging, filepath.FromSlash(c.KeyFile)), c.Producer.Password)
		if err != nil {
			return res, fmt.Errorf("archived key file does not open with the archived password: %w", err)
		}
		if !strings.EqualFold(addr, c.Producer.Address) {
			return res, fmt.Errorf("archived key file holds %s but the archived config expects %s", addr, c.Producer.Address)
		}
		res.Address = addr
	}

	safety := filepath.Join(backup.RestoreDir(cfg), fmt.Sprintf("wallet-safety.%d", now.Unix()))
	if err := os.MkdirAll(safety, 0o700); err != nil {
		return res, err
	}
	res.SafetyDir = safety
	for _, rel := range c.Files {
		live := filepath.Join(cfg.ZnnDir, filepath.FromSlash(rel))
		if _, err := os.Lstat(live); err == nil {
			keep := filepath.Join(safety, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(keep), 0o700); err != nil {
				return res, err
			}
			if err := os.Rename(live, keep); err != nil {
				return res, fmt.Errorf("move %s aside: %w", rel, err)
			}
		}
	}
	for _, rel := range c.Files {
		src := filepath.Join(staging, filepath.FromSlash(rel))
		dst := filepath.Join(cfg.ZnnDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return res, err
		}
		if err := os.Rename(src, dst); err != nil {
			return res, fmt.Errorf("install %s: %w", rel, err)
		}
	}
	if restartNode && res.WasRunning {
		if err := restart(cfg.ServiceName); err != nil {
			return res, err
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
