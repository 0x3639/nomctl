package lock

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestAcquireRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nomctl.lock")
	guard := filepath.Join(t.TempDir(), "blocked")
	l, err := acquire(path, "backup", guard)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(data), "backup pid ") {
		t.Errorf("holder not recorded: %q", data)
	}
	l.Release()
	l2, err := acquire(path, "restore", guard)
	if err != nil {
		t.Fatalf("lock should be free after Release: %v", err)
	}
	l2.Release()
	l2.Release() // idempotent
}

func TestMigrationGuardPersistsBeyondLockHolder(t *testing.T) {
	dir := t.TempDir()
	path, guard := filepath.Join(dir, "lock"), filepath.Join(dir, "blocked")
	l, err := acquire(path, "migration", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(guard, []byte("migration-id"), 0600); err != nil {
		t.Fatal(err)
	}
	l.Release()
	for _, operation := range []string{"scheduled backup", "start", "restore"} {
		if l, err := acquire(path, operation, guard); err == nil {
			l.Release()
			t.Fatalf("%s bypassed migration", operation)
		}
	}
	l, err = acquire(path, "resume migration", "")
	if err != nil {
		t.Fatal(err)
	}
	l.Release()
	if err := os.Remove(guard); err != nil {
		t.Fatal(err)
	}
	l, err = acquire(path, "start after handoff", guard)
	if err != nil {
		t.Fatal(err)
	}
	l.Release()
	// An unreadable or dangling sentinel must fail closed too.
	if err := os.Symlink(filepath.Join(dir, "missing"), guard); err != nil {
		t.Fatal(err)
	}
	if l, err := acquire(path, "start", guard); err == nil {
		l.Release()
		t.Fatal("dangling guard bypassed")
	}
}

// TestContention holds the lock from another process (flock is per open
// file description, so a second Acquire in the same process would also
// conflict, but a separate process is the real scenario).
func TestContention(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nomctl.lock")
	guard := filepath.Join(t.TempDir(), "blocked")
	if _, err := exec.LookPath("flock"); err != nil {
		// macOS has no flock(1); hold it from this process instead.
		l, err := acquire(path, "backup", guard)
		if err != nil {
			t.Fatal(err)
		}
		defer l.Release()
		if _, err := acquire(path, "restore", guard); err == nil || !strings.Contains(err.Error(), "backup pid") {
			t.Errorf("second acquire should fail naming the holder, got %v", err)
		}
		return
	}
	holder := exec.Command("flock", path, "sleep", "5")
	if err := holder.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Process.Kill(); _ = holder.Wait() }()
	// Give flock a moment to take the lock.
	for range 50 {
		l, err := acquire(path, "restore", guard)
		if err != nil {
			if !strings.Contains(err.Error(), "in progress") {
				t.Fatalf("unexpected error: %v", err)
			}
			return
		}
		l.Release()
		time.Sleep(10 * time.Millisecond)
	}
	t.Error("expected contention")
}

func TestMigrationGuardErrorPreservesCauseAndReleasesLock(t *testing.T) {
	dir := t.TempDir()
	parent := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(parent, nil, 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "lock")
	if l, err := acquire(path, "backup", filepath.Join(parent, "blocked")); l != nil || !errors.Is(err, syscall.ENOTDIR) {
		if l != nil {
			l.Release()
		}
		t.Fatalf("guard lookup should fail closed with its cause: %v", err)
	}
	l, err := AcquireMigration(path, "inspect migration")
	if err != nil {
		t.Fatalf("guard lookup failure leaked the lock: %v", err)
	}
	l.Release()
}
