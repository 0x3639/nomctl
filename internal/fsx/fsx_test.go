package fsx

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRenameExisting(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "go-zenon")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 6, 1, 2, 3, 0, time.UTC)
	if err := renameExisting(dir, now); err != nil {
		t.Fatal(err)
	}
	if Exists(dir) || !IsDir(dir+"-20260906010203") {
		t.Errorf("directory not renamed with timestamp suffix")
	}
	if err := renameExisting(filepath.Join(root, "missing"), now); err != nil {
		t.Errorf("missing dir should be a no-op, got %v", err)
	}
}

func TestCopyFile(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	if err := os.WriteFile(src, []byte("binary"), 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(root, "bin", "znnd")
	if err := CopyFile(src, dst, 0o755); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dst)
	if err != nil || info.Mode().Perm() != 0o755 || info.Size() != 6 {
		t.Errorf("copy result wrong: %v %v", info, err)
	}
}
