//go:build live

package nodeupgrade

import (
	"context"
	"os"
	"runtime"
	"testing"
	"time"
)

// This opt-in test downloads/verifies/extracts only; it never installs or executes
// a release or touches a service. NOMCTL_GITHUB_TOKEN is required while private.
func TestLiveReleaseDownloadAndVerification(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	client := NewClient(os.Getenv("NOMCTL_GITHUB_TOKEN"))
	releases, err := client.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// A short path avoids GnuPG Unix-socket limits on macOS test hosts.
	dir, err := os.MkdirTemp("/tmp", "nomctl-live-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	binary, err := client.Fetch(ctx, releases[0], dir, "linux", runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(binary)
	if err != nil || st.Size() == 0 {
		t.Fatal("missing extracted binary", err)
	}
	t.Logf("Verified and extracted %s for linux/%s (%d bytes); nothing installed or executed", releases[0].Tag, runtime.GOARCH, st.Size())
}
