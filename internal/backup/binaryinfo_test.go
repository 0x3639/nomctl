package backup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSavedBinaryInfo(t *testing.T) {
	for _, tc := range []struct {
		name, output, version, commit string
	}{
		{"znnd", "Starting znnd.\nversion: v0.0.8\ngit-commit-hash: abc123\nznnd\nVersion:v0.0.8\nGo Version:go1.26\nCommit hash:abc123\n", "v0.0.8", "abc123"},
		{"missing", "Version:\nCommit hash:\n", "", ""},
		{"unrelated", "Go Version:go1.26\nOperating System:linux\n", "", ""},
		{"control characters", "Version:v1\x1b[31m\nCommit hash:bad\x01value\n", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			version, commit := parseBinaryInfo(tc.output)
			if version != tc.version || commit != tc.commit {
				t.Fatalf("identity: %q %q", version, commit)
			}
		})
	}
	path := filepath.Join(t.TempDir(), "znnd")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n[ \"$1\" = version ] || exit 1\nprintf 'Version:v0.0.8\\nCommit hash:abc123\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	version, commit := savedBinaryInfo(path)
	if version != "v0.0.8" || commit != "abc123" {
		t.Fatalf("saved executable identity: %q %q", version, commit)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	version, commit = savedBinaryInfo(path)
	if version != "" || commit != "" {
		t.Fatal("unavailable version was invented")
	}
}

func TestRestorePointStoresSavedBinaryIdentity(t *testing.T) {
	cfg := testConfig(t)
	cfg.InstallDir = t.TempDir()
	mockCapacity(t, func(string) (int64, int64, error) { return 1 << 40, 0, nil })
	oldBinary := "#!/bin/sh\nprintf 'Version:v0.0.8\\nCommit hash:abc123\\n'\n"
	if err := os.WriteFile(cfg.BinaryPath(), []byte(oldBinary), 0o700); err != nil {
		t.Fatal(err)
	}
	snapshot, err := PrepareRestorePoint(cfg, "before upgrade")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = snapshot.Discard() })
	if err := os.WriteFile(cfg.BinaryPath(), []byte("new binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	p, err := ReadRestorePoint(cfg, snapshot.point.ID)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := SHA256File(p.BinaryPath())
	if err != nil || p.BinarySHA256 != hash || p.BinaryVersion != "v0.0.8" || p.BinaryCommit != "abc123" {
		t.Fatalf("saved identity changed: %+v, %v", p, err)
	}
	legacy := RestorePoint{BinarySHA256: hash}
	if !strings.Contains(legacy.BinaryDescription(), "version: unavailable") || !strings.Contains(legacy.BinaryDescription(), hash) {
		t.Fatal("legacy restore point identity is misleading")
	}
}
