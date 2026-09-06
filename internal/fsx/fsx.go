// Package fsx contains small filesystem and download helpers shared by the
// deploy, backup and analytics packages.
package fsx

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/0x3639/nomctl/internal/logx"
)

// RenameExisting moves dir to dir-<timestamp> if it exists (utils.sh
// rename_existing_dir). It is a no-op when dir is absent.
func RenameExisting(dir string) error {
	return renameExisting(dir, time.Now())
}

func renameExisting(dir string, now time.Time) error {
	info, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !info.IsDir() {
		return nil
	}
	dest := fmt.Sprintf("%s-%s", dir, now.Format("20060102150405"))
	if err := os.Rename(dir, dest); err != nil {
		return fmt.Errorf("rename %s: %w", dir, err)
	}
	logx.Success(fmt.Sprintf("Renamed existing '%s' to '%s'.", dir, dest))
	return nil
}

// CopyFile copies src to dst with the given mode, creating parent dirs.
func CopyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

// Download fetches url into dest, failing on non-2xx responses.
func Download(url, dest string, timeout time.Duration) error {
	slog.Debug("download", "url", url, "dest", dest)
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("download %s: HTTP %s", url, resp.Status)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, resp.Body); err != nil {
		_ = out.Close()
		_ = os.Remove(dest)
		return fmt.Errorf("download %s: %w", url, err)
	}
	return out.Close()
}

// Exists reports whether path exists.
func Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// IsDir reports whether path is an existing directory.
func IsDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
