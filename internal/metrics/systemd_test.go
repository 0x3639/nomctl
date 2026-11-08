package metrics

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestServicePropsHonorsContext(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "systemctl"), []byte("#!/bin/sh\nexec sleep 5\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started := time.Now()
	if _, err := ReadServicePropsContext(ctx, "example.service"); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancelled query, got %v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("cancelled query took too long")
	}
}
