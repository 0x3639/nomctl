package resync

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWipePreservesWalletAndConfig(t *testing.T) {
	data := t.TempDir()
	for _, d := range []string{"network", "nom", "consensus", "log", "wallet", "cache"} {
		if err := os.MkdirAll(filepath.Join(data, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(data, "config.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if n, err := Wipe(data); n != 4 || err != nil {
		t.Errorf("deleted %d, want 4 (err %v)", n, err)
	}
	for _, kept := range []string{"wallet", "cache", "config.json"} {
		if _, err := os.Stat(filepath.Join(data, kept)); err != nil {
			t.Errorf("%s should be preserved", kept)
		}
	}
	for _, gone := range Dirs {
		if _, err := os.Stat(filepath.Join(data, gone)); !os.IsNotExist(err) {
			t.Errorf("%s should be deleted", gone)
		}
	}
	if n, err := Wipe(data); n != 0 || err != nil {
		t.Errorf("second wipe deleted %d (err %v)", n, err)
	}
}
