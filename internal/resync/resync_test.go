package resync

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0x3639/nomctl/internal/config"
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

func TestRunStopsBeforeWipingDuringTransitions(t *testing.T) {
	for _, state := range []string{"active", "activating", "deactivating", "reloading", "inactive"} {
		for _, stopFails := range []bool{false, true} {
			name := state
			if stopFails {
				name += "/stop-error"
			}
			t.Run(name, func(t *testing.T) {
				root := t.TempDir()
				cfg := config.Default()
				cfg.ZnnDir = filepath.Join(root, "data")
				chain := filepath.Join(cfg.ZnnDir, "nom")
				if err := os.MkdirAll(chain, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(chain, "old"), []byte("old"), 0o644); err != nil {
					t.Fatal(err)
				}
				t.Setenv("INITIAL_STATE", state)
				t.Setenv("STOPPED", filepath.Join(root, "stopped"))
				t.Setenv("CALLS", filepath.Join(root, "calls"))
				if stopFails {
					t.Setenv("STOP_FAILS", "1")
				} else {
					t.Setenv("STOP_FAILS", "0")
				}
				script := `#!/bin/sh
printf '%s\n' "$1" >> "$CALLS"
case "$1" in
  show)
    printf 'LoadState=loaded\n'
    if [ -f "$STOPPED" ]; then printf 'ActiveState=inactive\n'; else printf 'ActiveState=%s\n' "$INITIAL_STATE"; fi
    ;;
  stop)
    if [ "$STOP_FAILS" = 1 ]; then exit 1; fi
    : > "$STOPPED"
    ;;
  status|start) exit 0 ;;
  *) exit 1 ;;
esac
`
				if err := os.WriteFile(filepath.Join(root, "systemctl"), []byte(script), 0o755); err != nil {
					t.Fatal(err)
				}
				t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
				err := Run(cfg)
				if (err != nil) != stopFails {
					t.Fatalf("Run = %v; stopFails = %v", err, stopFails)
				}
				_, statErr := os.Stat(filepath.Join(chain, "old"))
				if stopFails && statErr != nil {
					t.Fatal("data changed after stop failure")
				}
				if !stopFails && !os.IsNotExist(statErr) {
					t.Errorf("data not wiped after verified stop: %v", statErr)
				}
				calls, err := os.ReadFile(filepath.Join(root, "calls"))
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(calls), "stop\n") {
					t.Error("resync skipped the stop job")
				}
				shouldRestart := !stopFails && (state == "active" || state == "activating" || state == "reloading")
				if strings.Contains(string(calls), "start\n") != shouldRestart {
					t.Errorf("restart behavior: %s", calls)
				}
			})
		}
	}
}
