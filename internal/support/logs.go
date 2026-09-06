package support

import (
	"compress/gzip"
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
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	if int64(len(data)) > maxBytes {
		data = data[int64(len(data))-maxBytes:]
	}
	return os.WriteFile(dst, data, 0o600)
}
