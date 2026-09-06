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
