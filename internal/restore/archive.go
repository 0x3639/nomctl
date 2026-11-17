package restore

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/0x3639/nomctl/internal/backup"
)

// Manifest describes what a backup archive holds.
type Manifest struct {
	// Folders present in the archive, in backup.Folders order.
	Folders []string `json:"folders"`
	// Uncompressed is the total size of the regular files.
	Uncompressed int64 `json:"uncompressed_bytes"`
	Files        int   `json:"files"`
	// StagingBytes includes conservative space for files and parent directories.
	StagingBytes int64 `json:"staging_bytes"`
}

// Inspect reads every entry of a backup archive and checks that it is an
// ordinary directory or regular file inside one of the chain-data folders.
// Anything else (config.json, wallet/, links, devices, absolute paths,
// traversal) is refused, so a restore can never touch files it did not make.
func Inspect(archive string) (Manifest, error) {
	f, err := os.Open(archive)
	if err != nil {
		return Manifest{}, err
	}
	defer f.Close()
	return InspectReader(f)
}

// InspectReader validates a complete compressed stream, including its trailer.
// Callers can wrap the reader to provide cancellation and use an open snapshot.
func InspectReader(r io.Reader) (Manifest, error) {
	var m Manifest
	present := map[string]bool{}
	err := walkReader(r, func(h *tar.Header, _ io.Reader) error {
		folder, rel, err := entryPath(h.Name)
		if err != nil {
			return err
		}
		if folder == "" {
			return nil // the archive root itself
		}
		overhead := int64(strings.Count(h.Name, "/")+1) * (64 << 10)
		if h.Size > math.MaxInt64-overhead-m.StagingBytes {
			return errors.New("archive expanded size exceeds supported limits")
		}
		m.StagingBytes += h.Size + overhead
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
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	return ExtractReader(f, dst, nil)
}

// ExtractReader extracts into a new private directory. beforeWrite can enforce
// free-space limits before every directory creation and file-write chunk.
func ExtractReader(source io.Reader, dst string, beforeWrite func(int64) error) error {
	return walkReader(source, func(h *tar.Header, r io.Reader) error {
		folder, rel, err := entryPath(h.Name)
		if err != nil {
			return err
		}
		if folder == "" {
			return nil
		}
		if beforeWrite != nil {
			if err := beforeWrite(int64(strings.Count(h.Name, "/")+1) * (64 << 10)); err != nil {
				return err
			}
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
			if _, err := io.CopyBuffer(guardedWriter{out, beforeWrite}, r, make([]byte, 1<<20)); err != nil {
				_ = out.Close()
				return fmt.Errorf("extract %s: %w", h.Name, err)
			}
			return out.Close()
		default:
			return fmt.Errorf("backup entry %s is not a file or directory; refusing to restore", h.Name)
		}
	})
}

type guardedWriter struct {
	w     io.Writer
	check func(int64) error
}

func (w guardedWriter) Write(p []byte) (int, error) {
	if w.check != nil {
		if err := w.check(int64(len(p))); err != nil {
			return 0, err
		}
	}
	return w.w.Write(p)
}

func walkReader(source io.Reader, fn func(*tar.Header, io.Reader) error) error {
	gz, err := gzip.NewReader(source)
	if err != nil {
		return fmt.Errorf("not a gzip archive: %w", err)
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	seen, directories := map[string]bool{}, map[string]bool{}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			// tar EOF precedes the gzip checksum. Consume all padding and the
			// trailer; another tar stream or non-padding data is not accepted.
			_, err := io.Copy(zeroPadding{}, gz)
			return err
		}
		if err != nil {
			return fmt.Errorf("read archive: %w", err)
		}
		if _, _, err := entryPath(h.Name); err != nil {
			return err
		}
		name := path.Clean(h.Name)
		isDir := h.Typeflag == tar.TypeDir
		if !isDir && h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeRegA { //nolint:staticcheck // legacy tar format
			return fmt.Errorf("backup entry %s is not a file or directory", h.Name)
		}
		if name == "." && !isDir {
			return errors.New("archive root must be a directory")
		}
		if _, exists := seen[name]; exists || (!isDir && directories[name]) {
			return fmt.Errorf("duplicate or conflicting archive entry %s", h.Name)
		}
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			if dir, exists := seen[parent]; exists && !dir {
				return fmt.Errorf("archive parent %s is a file", parent)
			}
			directories[parent] = true
		}
		seen[name] = isDir
		if err := fn(h, tr); err != nil {
			return err
		}
	}
}

type zeroPadding struct{}

func (zeroPadding) Write(p []byte) (int, error) {
	for _, b := range p {
		if b != 0 {
			return 0, errors.New("unexpected data after the end of the tar archive")
		}
	}
	return len(p), nil
}

// entryPath maps a tar entry name to (chain-data folder, path inside it).
// The archive root ("." or "./") maps to an empty folder.
func entryPath(name string) (string, string, error) {
	if path.IsAbs(name) || strings.ContainsRune(name, '\\') {
		return "", "", fmt.Errorf("unsafe archive path: %s", name)
	}
	for _, component := range strings.Split(name, "/") {
		if component == ".." {
			return "", "", fmt.Errorf("backup entry escapes its directory: %s", name)
		}
	}
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
