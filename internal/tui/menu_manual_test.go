package tui

import (
	"os"
	"testing"

	"github.com/0x3639/nomctl/internal/config"
)

// TestManualRun drives the real menu when NOMCTL_TUI_MANUAL=1 (used by the
// expect-based smoke test; skipped otherwise).
func TestManualRun(t *testing.T) {
	if os.Getenv("NOMCTL_TUI_MANUAL") != "1" {
		t.Skip("manual")
	}
	cfg := config.Default()
	if err := Run(cfg); err != nil {
		t.Fatal(err)
	}
}
