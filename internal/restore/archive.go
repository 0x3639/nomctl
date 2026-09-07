package restore

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/0x3639/nomctl/internal/backup"
)

// Manifest describes what a backup archive holds.
type Manifest struct {
	// Folders present in the archive, in backup.Folders order.
	Folders []string
	// Uncompressed is the total size of the regular files.
	Uncompressed int64
	Files        int
}

// Inspect reads every entry of a backup archive and checks that it is an
// ordinary directory or regular file inside one of the chain-data folders.
// Anything else (config.json, wallet/, links, devices, absolute paths,
// traversal) is refused, so a restore can never touch files it did not make.
func Inspect(archive string) (Manifest, error) {
	var m Manifest
	present := map[string]bool{}
	err := walk(archive, func(h *tar.Header, _ io.Reader) error {
		folder, rel, err := entryPath(h.Name)
		if err != nil {
			return err
		}
		if folder == "" {
			return nil // the archive root itself
		}
		switch h.Typeflag {
		case tar.TypeDir:
		case tar.TypeReg, tar.TypeRegA: //nolint:staticcheck // TypeRegA is what older tar writers emit
			if rel == "" {
				return fmt.Errorf("backup entry %s is a file where the %s directory should be; refusing to restore", h.Name, folder)
			}
			m.Files++
			m.Uncompressed += h.Size
		default:
			return fmt.Errorf("backup entry %s is not a file or directory (type %q); refusing to restore", h.Name, h.Typeflag)
		}
		present[folder] = true
		return nil
	})
	if err != nil {
		return Manifest{}, err
	}
	for _, f := range backup.Folders {
		if present[f] {
			m.Folders = append(m.Folders, f)
		}
	}
	if len(m.Folders) == 0 {
		return Manifest{}, errors.New("backup holds none of the chain-data folders")
	}
	return m, nil
}

// Extract unpacks the archive's chain-data folders under dst. Inspect must
// have accepted the archive first; Extract applies the same checks again.
func Extract(archive, dst string) error {
	return walk(archive, func(h *tar.Header, r io.Reader) error {
		folder, rel, err := entryPath(h.Name)
		if err != nil {
			return err
		}
		if folder == "" {
			return nil
		}
		target := filepath.Join(dst, folder, filepath.FromSlash(rel))
		switch h.Typeflag {
		case tar.TypeDir:
			return os.MkdirAll(target, 0o755)
		case tar.TypeReg, tar.TypeRegA: //nolint:staticcheck // see Inspect
			if rel == "" {
				return fmt.Errorf("backup entry %s is a file where the %s directory should be; refusing to restore", h.Name, folder)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			mode := os.FileMode(h.Mode).Perm() //nolint:gosec // tar modes fit
			if mode == 0 {
				mode = 0o644
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC|os.O_EXCL, mode)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, r); err != nil {
				_ = out.Close()
				return fmt.Errorf("extract %s: %w", h.Name, err)
			}
			return out.Close()
		default:
			return fmt.Errorf("backup entry %s is not a file or directory; refusing to restore", h.Name)
		}
	})
}

func walk(archive string, fn func(*tar.Header, io.Reader) error) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("%s is not a gzip archive: %w", filepath.Base(archive), err)
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read %s: %w", filepath.Base(archive), err)
		}
		if err := fn(h, tr); err != nil {
			return err
		}
	}
}

// entryPath maps a tar entry name to (chain-data folder, path inside it).
// The archive root ("." or "./") maps to an empty folder.
func entryPath(name string) (string, string, error) {
	clean := path.Clean("/" + name) // resolves . and .. against a virtual root
	if strings.HasPrefix(clean, "/..") {
		return "", "", fmt.Errorf("backup entry escapes the archive: %s", name)
	}
	clean = strings.TrimPrefix(clean, "/")
	if clean == "" || clean == "." {
		return "", "", nil
	}
	folder, rel, _ := strings.Cut(clean, "/")
	for _, f := range backup.Folders {
		if folder == f {
			return folder, rel, nil
		}
	}
	return "", "", fmt.Errorf("backup entry %s is outside the chain-data folders %v; refusing to restore", name, backup.Folders)
}
