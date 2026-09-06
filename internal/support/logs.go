package support

import (
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Limits carried over from the script.
const (
	MaxLogFiles  = 30
	LogTailBytes = 4 << 20
	maxLogDepth  = 3
)

// LogFile describes one file under the node's log directory.
type LogFile struct {
	Path    string
	Size    int64
	ModTime time.Time
}

// ListLogs returns regular files up to three levels deep, newest first.
func ListLogs(logDir string) ([]LogFile, error) {
	if _, err := os.Stat(logDir); err != nil {
		return nil, err
	}
	var logs []LogFile
	err := filepath.WalkDir(logDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entries are skipped
		}
		rel, _ := filepath.Rel(logDir, path)
		depth := len(strings.Split(rel, string(filepath.Separator)))
		if d.IsDir() {
			if path != logDir && depth >= maxLogDepth {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}
		logs = append(logs, LogFile{Path: path, Size: info.Size(), ModTime: info.ModTime()})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(logs, func(i, j int) bool { return logs[i].ModTime.After(logs[j].ModTime) })
	return logs, nil
}

// TailLog writes the last maxBytes of src (decompressing .gz) to dst.
func TailLog(src, dst string, maxBytes int64) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	var r io.Reader = f
	if strings.HasSuffix(src, ".gz") {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return err
		}
		defer func() { _ = gz.Close() }()
		r = gz
	} else if info, err := f.Stat(); err == nil && info.Size() > maxBytes {
		if _, err := f.Seek(info.Size()-maxBytes, io.SeekStart); err != nil {
			return err
		}
	}
	data, err := tailBytes(r, maxBytes)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o600)
}

// tailBytes returns at most maxBytes from the end of r without holding more
// than about 2*maxBytes in memory, so a multi-gigabyte rotated log cannot
// exhaust a host that is already under memory pressure.
func tailBytes(r io.Reader, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		return nil, nil
	}
	buf := make([]byte, 0, maxBytes)
	chunk := make([]byte, 64<<10)
	for {
		n, err := r.Read(chunk)
		if n > 0 {
			buf = append(buf, chunk[:n]...)
			if int64(len(buf)) > maxBytes {
				buf = append(buf[:0], buf[int64(len(buf))-maxBytes:]...)
			}
		}
		if errors.Is(err, io.EOF) {
			return buf, nil
		}
		if err != nil {
			return nil, err
		}
	}
}
