package support

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
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

// TailLog writes a bounded, redacted tail of src (decompressing .gz) to a new dst.
func TailLog(src, dst string, maxBytes int64) error {
	return tailLogContext(context.Background(), src, dst, maxBytes)
}

func tailLogContext(ctx context.Context, src, dst string, maxBytes int64) error {
	data, err := readLogTail(ctx, src, maxBytes)
	if err != nil {
		return err
	}
	return writePrivateFile(dst, []byte(Redact(string(data))))
}

func readLogTail(ctx context.Context, src string, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	f, err := os.OpenFile(src, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("log is not a regular file")
	}
	var data []byte
	truncated := false
	if strings.HasSuffix(src, ".gz") {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return nil, err
		}
		defer func() { _ = gz.Close() }()
		data, err = tailBytes(contextReader{ctx, gz}, maxBytes+1)
		if err != nil {
			return nil, err
		}
		truncated = int64(len(data)) > maxBytes
		if truncated {
			data = data[len(data)-int(maxBytes):]
		}
	} else {
		truncated = info.Size() > maxBytes
		if truncated {
			if _, err := f.Seek(info.Size()-maxBytes, io.SeekStart); err != nil {
				return nil, err
			}
		}
		data, err = io.ReadAll(io.LimitReader(contextReader{ctx, f}, maxBytes))
		if err != nil {
			return nil, err
		}
	}
	// Do not retain a record whose secret key might have been cut off.
	if truncated {
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			data = data[i+1:]
		} else {
			data = nil
		}
		// A multiline structured record may begin before the retained tail.
		// Omit its leading continuation fields rather than keep an orphaned
		// value after dropping the field name at the boundary.
		for len(data) > 0 {
			line := data
			next := len(data)
			if i := bytes.IndexByte(data, '\n'); i >= 0 {
				line, next = data[:i], i+1
			}
			trimmed := bytes.TrimSpace(line)
			if len(trimmed) > 0 && !bytes.ContainsAny(trimmed[:1], "\"'}]") {
				break
			}
			data = data[next:]
		}
		data = append([]byte("[log truncated; showing complete tail lines]\n"), data...)
	}
	return []byte(Redact(string(data))), nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
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
