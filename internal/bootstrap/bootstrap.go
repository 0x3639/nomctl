// Package bootstrap replaces the node's chain data with a published snapshot
// so a fresh node is in sync within minutes. It ports the hypercore
// restore-from-bootstrap script: download a zip and its .hash sidecar, verify
// the SHA-256, swap the data directories and restart the service.
package bootstrap

import (
	"archive/zip"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/0x3639/nomctl/internal/backup"
	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/fsx"
	"github.com/0x3639/nomctl/internal/logx"
	"github.com/0x3639/nomctl/internal/restore"
	"github.com/0x3639/nomctl/internal/service"
)

// Dirs are the data directories a snapshot supplies. Inside the zip each one
// lives under backup/<dir>.bak/.
var Dirs = []string{"nom", "network", "consensus"}

// ConfirmText is the warning shown before an interactive bootstrap.
const ConfirmText = "This replaces the node's chain data with the\ndownloaded snapshot and restarts the service.\n\nThe wallet and config.json are not touched.\n\nContinue?"

// ErrNoURL is returned when neither the argument nor NOMCTL_BOOTSTRAP_URL
// names a snapshot.
var ErrNoURL = errors.New("no snapshot URL given; pass one or set NOMCTL_BOOTSTRAP_URL")

// Options control Run.
type Options struct {
	// URL of the .zip snapshot. The hash sidecar is derived from it.
	URL string
	// Discard deletes the current chain data instead of keeping a copy in
	// the restore directory. Use it on disks too small for both.
	Discard bool
	// Now is the clock (tests); nil means time.Now.
	Now func() time.Time
}

// Hooks that Run goes through so tests can stub the host.
var (
	stopService  = service.Stop
	startService = service.Start
	diskFree     = fsx.DiskFree
	installDirs  = install
	httpClient   = newClient()
)

// newClient bounds how long a peer may withhold response headers without
// capping the body transfer, which takes as long as the snapshot is large.
func newClient() *http.Client {
	t, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return &http.Client{}
	}
	t = t.Clone()
	t.ResponseHeaderTimeout = 30 * time.Second
	return &http.Client{Transport: t}
}

// HashURL returns the sidecar URL: the archive URL with its extension
// replaced by .hash.
func HashURL(archiveURL string) string {
	u, err := url.Parse(archiveURL)
	if err != nil {
		return strings.TrimSuffix(archiveURL, path.Ext(archiveURL)) + ".hash"
	}
	u.Path = strings.TrimSuffix(u.Path, path.Ext(u.Path)) + ".hash"
	return u.String()
}

// ParseHash extracts the SHA-256 from a sidecar: either the bare hex digest
// or a sha256sum-style "<digest>  <file>" line.
func ParseHash(data []byte) (string, error) {
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return "", errors.New("hash file is empty")
	}
	h := strings.ToLower(fields[0])
	if len(h) != 64 {
		return "", fmt.Errorf("hash file does not hold a SHA-256 digest: %q", fields[0])
	}
	if _, err := hex.DecodeString(h); err != nil {
		return "", fmt.Errorf("hash file does not hold a SHA-256 digest: %q", fields[0])
	}
	return h, nil
}

// ValidateURL accepts http and https URLs with a host and a .zip path.
func ValidateURL(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return ErrNoURL
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("snapshot URL: %w", err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("snapshot URL must be http(s)://host/path.zip, got %q", raw)
	}
	if !strings.EqualFold(path.Ext(u.Path), ".zip") {
		return fmt.Errorf("snapshot URL must point at a .zip, got %q", raw)
	}
	return nil
}

// Manifest describes a snapshot archive.
type Manifest struct {
	// Uncompressed is the total size of the data once extracted.
	Uncompressed int64
	// Files per data directory.
	Files map[string]int
}

// Inspect reads the zip's central directory and checks that every entry
// lives under backup/<dir>.bak/ for a known dir, with no path traversal, and
// that each dir has at least one entry.
func Inspect(zipPath string) (Manifest, error) {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return Manifest{}, fmt.Errorf("open snapshot: %w", err)
	}
	defer func() { _ = r.Close() }()
	m := Manifest{Files: map[string]int{}}
	for _, f := range r.File {
		dir, _, err := split(f.Name)
		if err != nil {
			return Manifest{}, err
		}
		if f.Mode()&os.ModeSymlink != 0 {
			return Manifest{}, fmt.Errorf("snapshot contains a symlink: %s", f.Name)
		}
		if dir == "" || f.FileInfo().IsDir() {
			continue
		}
		m.Files[dir]++
		m.Uncompressed += int64(f.UncompressedSize64) //nolint:gosec // sizes are far below MaxInt64
	}
	for _, d := range Dirs {
		if m.Files[d] == 0 {
			return Manifest{}, fmt.Errorf("snapshot has no files under backup/%s.bak/", d)
		}
	}
	return m, nil
}

// split maps a zip entry name to (dir, relative path under that dir).
func split(name string) (string, string, error) {
	clean := path.Clean(name)
	if path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", "", fmt.Errorf("snapshot entry escapes the archive: %s", name)
	}
	if clean == "backup" {
		return "", "", nil // the wrapper directory itself
	}
	parts := strings.SplitN(clean, "/", 3)
	if parts[0] != "backup" || len(parts) < 2 {
		return "", "", fmt.Errorf("unexpected snapshot entry (want backup/<dir>.bak/...): %s", name)
	}
	dir := strings.TrimSuffix(parts[1], ".bak")
	if dir == parts[1] || !known(dir) {
		return "", "", fmt.Errorf("unexpected snapshot entry (want backup/<dir>.bak/...): %s", name)
	}
	rel := ""
	if len(parts) == 3 {
		rel = parts[2]
	}
	return dir, rel, nil
}

func known(dir string) bool {
	for _, d := range Dirs {
		if d == dir {
			return true
		}
	}
	return false
}

// Extract unpacks the snapshot so that dst/<dir> holds the contents of
// backup/<dir>.bak/ for each data directory.
func Extract(zipPath, dst string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("open snapshot: %w", err)
	}
	defer func() { _ = r.Close() }()
	for _, f := range r.File {
		dir, rel, err := split(f.Name)
		if err != nil {
			return err
		}
		if dir == "" {
			continue
		}
		target := filepath.Join(dst, dir, filepath.FromSlash(rel))
		if f.FileInfo().IsDir() || rel == "" {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := extractFile(f, target); err != nil {
			return fmt.Errorf("extract %s: %w", f.Name, err)
		}
	}
	return nil
}

func extractFile(f *zip.File, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	in, err := f.Open()
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	mode := f.Mode().Perm()
	if mode == 0 {
		mode = 0o644
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// Download fetches url into dest, reporting progress through report (bytes
// done, total or -1 when unknown) roughly every five seconds. A partial file
// is removed on error.
func Download(ctx context.Context, url, dest string, report func(done, total int64)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	res, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %s", url, res.Status)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	w := &progressWriter{w: out, total: res.ContentLength, report: report, last: time.Now()}
	if _, err := io.Copy(w, res.Body); err != nil {
		_ = out.Close()
		_ = os.Remove(dest)
		return fmt.Errorf("download %s: %w", url, err)
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(dest)
		return err
	}
	if report != nil {
		report(w.done, w.total)
	}
	return nil
}

type progressWriter struct {
	w      io.Writer
	done   int64
	total  int64
	report func(done, total int64)
	last   time.Time
}

func (p *progressWriter) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	p.done += int64(n)
	if p.report != nil && time.Since(p.last) >= 5*time.Second {
		p.last = time.Now()
		p.report(p.done, p.total)
	}
	return n, err
}

// RemoteSize returns the Content-Length reported by a HEAD request, or -1.
func RemoteSize(ctx context.Context, url string) int64 {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return -1
	}
	res, err := httpClient.Do(req)
	if err != nil {
		return -1
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return -1
	}
	return res.ContentLength
}

// LogProgress is a Download report callback that logs percentage and MB.
func LogProgress(done, total int64) {
	const mb = 1024 * 1024
	if total > 0 {
		slog.Info(fmt.Sprintf("Downloading… %d%% (%d of %d MB)", done*100/total, done/mb, total/mb))
		return
	}
	slog.Info(fmt.Sprintf("Downloading… %d MB", done/mb))
}

// ensureSpace checks that dir has needed bytes plus the configured margin.
func ensureSpace(dir string, needed int64, marginKB int64, what string) error {
	availKB, _, err := diskFree(dir)
	if err != nil {
		return fmt.Errorf("check disk space at %s: %w", dir, err)
	}
	neededKB := needed/1024 + marginKB
	if availKB < neededKB {
		return fmt.Errorf("not enough disk space at %s for %s: %d MB available, %d MB needed (including the %d MB NOMCTL_MIN_FREE_SPACE_KB margin)",
			dir, what, availKB/1024, neededKB/1024, marginKB/1024)
	}
	return nil
}

// Run downloads, verifies and installs the snapshot named by opts.URL, then
// restarts the service.
func Run(ctx context.Context, cfg config.Config, opts Options) error {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if err := ValidateURL(opts.URL); err != nil {
		return err
	}
	dlDir := filepath.Join(cfg.BackupDir, "bootstrap")
	archive := filepath.Join(dlDir, path.Base(opts.URL))
	hashFile := strings.TrimSuffix(archive, path.Ext(archive)) + ".hash"

	if err := os.MkdirAll(dlDir, 0o755); err != nil {
		return err
	}
	if err := Download(ctx, HashURL(opts.URL), hashFile, nil); err != nil {
		return fmt.Errorf("the .hash sidecar must sit next to the snapshot: %w", err)
	}
	hashData, err := os.ReadFile(hashFile)
	if err != nil {
		return err
	}
	want, err := ParseHash(hashData)
	if err != nil {
		return err
	}

	if fsx.Exists(archive) && hashMatches(archive, want) {
		slog.Info("Snapshot already downloaded and verified; skipping download")
	} else {
		_ = os.Remove(archive)
		if size := RemoteSize(ctx, opts.URL); size > 0 {
			if err := ensureSpace(dlDir, size, cfg.MinFreeSpaceKB, "the download"); err != nil {
				return err
			}
			slog.Info(fmt.Sprintf("Downloading %s (%d MB)", opts.URL, size/1024/1024))
		} else {
			slog.Info("Downloading " + opts.URL)
		}
		if err := Download(ctx, opts.URL, archive, LogProgress); err != nil {
			return err
		}
		if !hashMatches(archive, want) {
			_ = os.Remove(archive)
			return errors.New("checksum verification failed: the download does not match its .hash sidecar")
		}
		logx.Success("Download complete and checksum verified")
	}

	manifest, err := Inspect(archive)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.ZnnDir, 0o755); err != nil {
		return err
	}
	staging := filepath.Join(cfg.ZnnDir, fmt.Sprintf(".bootstrap-%d", opts.Now().Unix()))
	defer func() { _ = os.RemoveAll(staging) }()

	if opts.Discard {
		// The old data is deleted before extraction to make room, so it
		// cannot be rolled back: check space (counting what will be freed)
		// before stopping, and keep the verified archive so a failed run is
		// retried by running again.
		reclaim := dirsSize(cfg.ZnnDir, Dirs)
		if err := ensureSpace(cfg.ZnnDir, manifest.Uncompressed-reclaim, cfg.MinFreeSpaceKB, "the extracted snapshot"); err != nil {
			return err
		}
		if err := stopService(cfg.ServiceName); err != nil {
			return err
		}
		for _, d := range Dirs {
			if err := os.RemoveAll(filepath.Join(cfg.ZnnDir, d)); err != nil {
				return fmt.Errorf("discard %s: %w (the node is stopped; run bootstrap again)", d, err)
			}
		}
		slog.Info("Previous chain data discarded")
		if err := extractTo(archive, staging, manifest); err != nil {
			return fmt.Errorf("%w (the node is stopped and its chain data discarded; run bootstrap again)", err)
		}
		if err := installDirs(cfg, staging); err != nil {
			return fmt.Errorf("%w (the node is stopped; run bootstrap again)", err)
		}
	} else {
		if err := ensureSpace(cfg.ZnnDir, manifest.Uncompressed, cfg.MinFreeSpaceKB, "the extracted snapshot"); err != nil {
			return err
		}
		if err := extractTo(archive, staging, manifest); err != nil {
			return err
		}
		if err := stopService(cfg.ServiceName); err != nil {
			return err
		}
		moved, err := restore.MoveAside(cfg, opts.Now(), Dirs)
		if err == nil {
			err = installDirs(cfg, staging)
		}
		if err != nil {
			// Put the previous data back and restart so the node keeps
			// running on what it had.
			if backErr := restore.MoveBack(cfg, moved); backErr != nil {
				return fmt.Errorf("%w; and the previous data could not all be put back: %w (the node is stopped)", err, backErr)
			}
			if startErr := startService(cfg.ServiceName); startErr != nil {
				return fmt.Errorf("%w; previous data put back but the node did not start: %w", err, startErr)
			}
			return fmt.Errorf("%w; previous data put back and the node restarted", err)
		}
		for _, safetyCopy := range moved {
			if safetyCopy != "" {
				slog.Info("Previous chain data kept in " + backup.RestoreDir(cfg) + "; delete it once the node has synced")
				break
			}
		}
	}

	logx.Success("Snapshot installed; starting " + cfg.ServiceName)
	if err := startService(cfg.ServiceName); err != nil {
		return fmt.Errorf("%w (the snapshot is installed; the download is kept for a retry)", err)
	}
	_ = os.Remove(archive)
	_ = os.Remove(hashFile)
	return nil
}

// install renames the staged directories into the data directory. A failure
// part-way leaves the ones already installed in place; the caller decides
// whether to roll back.
func install(cfg config.Config, staging string) error {
	for _, d := range Dirs {
		if err := os.Rename(filepath.Join(staging, d), filepath.Join(cfg.ZnnDir, d)); err != nil {
			return fmt.Errorf("install %s: %w", d, err)
		}
	}
	return nil
}

// dirsSize sums the file sizes under the named directories; errors count 0.
func dirsSize(root string, dirs []string) int64 {
	var total int64
	for _, d := range dirs {
		_ = filepath.WalkDir(filepath.Join(root, d), func(_ string, e os.DirEntry, err error) error {
			if err != nil || e.IsDir() {
				return nil
			}
			if info, err := e.Info(); err == nil {
				total += info.Size()
			}
			return nil
		})
	}
	return total
}

func extractTo(archive, staging string, m Manifest) error {
	slog.Info(fmt.Sprintf("Extracting snapshot (%d MB)…", m.Uncompressed/1024/1024))
	if err := Extract(archive, staging); err != nil {
		return err
	}
	return nil
}

func hashMatches(archive, want string) bool {
	got, err := backup.SHA256File(archive)
	return err == nil && strings.EqualFold(got, want)
}
