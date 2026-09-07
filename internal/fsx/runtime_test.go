//go:build unix

package fsx

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRuntimeFilePublication(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	existing := filepath.Join(t.TempDir(), "example.json")
	if err := os.WriteFile(existing, []byte("example content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(existing, path); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRuntimeFile(path, 100); err == nil {
		t.Fatal("linked runtime file accepted")
	}
	if err := WriteRuntimeFile(path, []byte(`{"value":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(existing); string(data) != "example content" {
		t.Fatal("existing linked target changed")
	}
	if st, err := os.Lstat(path); err != nil || !st.Mode().IsRegular() || st.Mode().Perm() != 0o644 {
		t.Fatalf("published runtime file: %v, %v", st, err)
	}
	if data, err := ReadRuntimeFile(path, 100); err != nil || string(data) != `{"value":1}` {
		t.Fatalf("runtime file = %q, %v", data, err)
	}
	if _, err := ReadRuntimeFile(path, 5); err == nil {
		t.Fatal("oversized runtime file accepted")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatal("temporary runtime files were left behind")
	}
}

func TestRuntimeFileRequiresPrivateWriterDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := WriteRuntimeFile(filepath.Join(dir, "state.json"), []byte("{}"), 0o600); err == nil {
		t.Fatal("shared writable runtime directory accepted")
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(t.TempDir(), "runtime")
	if err := os.Symlink(dir, linked); err != nil {
		t.Fatal(err)
	}
	if err := WriteRuntimeFile(filepath.Join(linked, "state.json"), []byte("{}"), 0o600); err == nil {
		t.Fatal("linked runtime directory accepted")
	}
}
