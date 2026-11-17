// Package lock serialises operations that stop the node or touch its data
// so a scheduled backup cannot overlap a manual backup, restore, resync or
// deploy. The lock is an exclusive flock on a file; the kernel releases it
// when the holder exits, crashes included.
package lock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// ErrBusy identifies lock contention separately from filesystem failures.
var ErrBusy = errors.New("another nomctl operation is in progress")

// DefaultPath is where the lock file lives. /run is tmpfs on systemd hosts,
// so a stale file never survives a reboot.
const DefaultPath = "/run/nomctl.lock"

// MigrationGuard persists across reboot while producer ownership is in transit.
const MigrationGuard = "/var/lib/nomctl/migration/blocked"

// Lock is a held lock.
type Lock struct {
	f *os.File
}

// File lets a supervised child inherit the held flock through exec.ExtraFiles.
// The child then keeps a transfer lock alive if its supervising process dies.
func (l *Lock) File() *os.File { return l.f }

// Acquire takes the exclusive lock at path without waiting. operation names
// what the caller is doing and is recorded in the file for the error message
// shown to whoever collides with it.
func Acquire(path, operation string) (*Lock, error) {
	return acquire(path, operation, MigrationGuard)
}

// AcquireMigration permits only the migration coordinator to resolve a durable
// handoff guard. Ordinary backup/restore/service operations always use Acquire.
func AcquireMigration(path, operation string) (*Lock, error) { return acquire(path, operation, "") }

func acquire(path, operation, guard string) (*Lock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		holder := describeHolder(f)
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w%s; try again later", ErrBusy, holder)
		}
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	// Record who holds it, for the message above.
	_ = f.Truncate(0)
	_, _ = f.WriteAt([]byte(fmt.Sprintf("%s pid %d\n", operation, os.Getpid())), 0)
	l := &Lock{f: f}
	if guard != "" {
		if _, err := os.Lstat(guard); !errors.Is(err, os.ErrNotExist) {
			l.Release()
			if err != nil {
				return nil, fmt.Errorf("check migration handoff guard %s: %w", guard, err)
			}
			return nil, fmt.Errorf("node operations are blocked by a persistent migration handoff; use 'nomctl migrate status', resume, or return before changing this node")
		}
	}
	return l, nil
}

// Release drops the lock.
func (l *Lock) Release() {
	if l == nil || l.f == nil {
		return
	}
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	_ = l.f.Close()
	l.f = nil
}

func describeHolder(f *os.File) string {
	buf := make([]byte, 128)
	n, _ := f.ReadAt(buf, 0)
	s := strings.TrimSpace(string(buf[:n]))
	if s == "" {
		return ""
	}
	return " (" + s + ")"
}
