package recovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// Identity excludes access time: verification itself may update it. Change time
// catches in-place edits even when a writer restores the old size and mtime.
type Identity struct {
	Path     string `json:"path"`
	Device   uint64 `json:"device"`
	Inode    uint64 `json:"inode"`
	Size     int64  `json:"size"`
	Mode     uint32 `json:"mode"`
	Modified int64  `json:"modified_ns"`
	Changed  int64  `json:"changed_ns"`
	Problem  string `json:"problem,omitempty"`
}

func identity(path string, st os.FileInfo, err error) Identity {
	i := Identity{Path: path}
	if err != nil {
		i.Problem = err.Error()
		return i
	}
	s, ok := st.Sys().(*syscall.Stat_t)
	if !ok {
		i.Problem = "file identity is unavailable"
		return i
	}
	i.Device, i.Inode = uint64(s.Dev), uint64(s.Ino) //nolint:unconvert // platform-dependent types
	i.Size, i.Mode, i.Modified, i.Changed = st.Size(), uint32(st.Mode()), st.ModTime().UnixNano(), changeTime(s)
	return i
}

func identify(path string) Identity {
	st, err := os.Lstat(path)
	return identity(path, st, err)
}

type component struct {
	file *os.File
	id   Identity
}

func openComponent(path string) (*component, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		_ = f.Close()
		return nil, errors.Join(err, fmt.Errorf("%s must be a regular file", path))
	}
	id := identity(path, st, nil)
	if id.Problem != "" || id != identify(path) {
		_ = f.Close()
		return nil, errors.New("file changed while opening it")
	}
	return &component{f, id}, nil
}

func (c *component) unchanged() bool {
	st, err := c.file.Stat()
	return identity(c.id.Path, st, err) == c.id && identify(c.id.Path) == c.id
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

func (c *component) reader(ctx context.Context) (io.Reader, error) {
	if _, err := c.file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	return contextReader{ctx, c.file}, nil
}

func (c *component) hash(ctx context.Context) (string, error) {
	r, err := c.reader(ctx)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (c *component) read(ctx context.Context, limit int64) ([]byte, error) {
	r, err := c.reader(ctx)
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err == nil && int64(len(data)) > limit {
		err = errors.New("metadata file exceeds size limit")
	}
	return data, err
}
