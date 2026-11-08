//go:build unix

package fsx

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// WriteRuntimeFile atomically replaces a runtime file in a directory owned by
// this process's user, with no group or other write permission. All operations
// after opening the directory are relative to that descriptor.
func WriteRuntimeFile(path string, data []byte, mode os.FileMode) error {
	dir, err := unix.Open(filepath.Dir(path), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(dir) }()
	var st unix.Stat_t
	if err := unix.Fstat(dir, &st); err != nil {
		return err
	}
	if int(st.Uid) != os.Geteuid() || st.Mode&0o022 != 0 {
		return fmt.Errorf("runtime directory must be owned by the current user and not writable by others")
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return err
	}
	tmp := ".nomctl-" + hex.EncodeToString(random[:])
	fd, err := unix.Openat(dir, tmp, unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Unlinkat(dir, tmp, 0) }()
	f := os.NewFile(uintptr(fd), tmp)
	defer func() { _ = f.Close() }()
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Chmod(mode); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return unix.Renameat(dir, tmp, dir, filepath.Base(path))
}

// ReadRuntimeFile reads a bounded regular file without following a final link
// or blocking on a special file. Runtime JSON is small; oversized files fail.
func ReadRuntimeFile(path string, maxBytes int64) ([]byte, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Size() > maxBytes {
		return nil, fmt.Errorf("runtime file must be a regular file of at most %d bytes", maxBytes)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("runtime file exceeds %d bytes", maxBytes)
	}
	return data, nil
}
