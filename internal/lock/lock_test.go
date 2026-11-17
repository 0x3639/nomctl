package lock

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAcquireRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nomctl.lock")
	l, err := Acquire(path, "backup")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(data), "backup pid ") {
		t.Errorf("holder not recorded: %q", data)
	}
	l.Release()
	l2, err := Acquire(path, "restore")
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
	if _, err := exec.LookPath("flock"); err != nil {
		// macOS has no flock(1); hold it from this process instead.
		l, err := Acquire(path, "backup")
		if err != nil {
			t.Fatal(err)
		}
		defer l.Release()
		if _, err := Acquire(path, "restore"); err == nil || !strings.Contains(err.Error(), "backup pid") {
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
		if _, err := Acquire(path, "restore"); err != nil {
			if !strings.Contains(err.Error(), "in progress") {
				t.Fatalf("unexpected error: %v", err)
			}
			return
		}
	}
	t.Error("expected contention")
}
