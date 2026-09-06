//go:build live

package bootstrap

import (
	"context"
	"os"
	"testing"

	"github.com/0x3639/nomctl/internal/config"
)

// go test -tags live ./internal/bootstrap -run TestLive
func TestLiveSidecarAndSize(t *testing.T) {
	u := config.DefaultBootstrapURL
	dest := t.TempDir() + "/snap.hash"
	if err := Download(context.Background(), HashURL(u), dest, nil); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(dest)
	h, err := ParseHash(data)
	if err != nil {
		t.Fatal(err)
	}
	size := RemoteSize(context.Background(), u)
	if size <= 0 {
		t.Fatalf("RemoteSize = %d", size)
	}
	t.Logf("hash %s, archive %d MB", h, size/1024/1024)
}
