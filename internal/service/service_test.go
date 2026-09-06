package service

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/0x3639/nomctl/internal/execx"
)

// stubSystemctl puts a fake systemctl on PATH that exits with the code in
// $STUB_EXIT (default 0).
func stubSystemctl(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\nexit ${STUB_EXIT:-0}\n"
	if err := os.WriteFile(filepath.Join(dir, "systemctl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	execx.Configure(false, nil)
}

func TestStatus(t *testing.T) {
	stubSystemctl(t)
	cases := []struct {
		exit    string
		want    State
		wantErr bool
	}{
		{"0", Active, false},
		{"3", Inactive, false},
		{"4", NotFound, false},
		{"1", Inactive, true},
	}
	for _, c := range cases {
		t.Setenv("STUB_EXIT", c.exit)
		got, err := Status("go-zenon")
		if got != c.want || (err != nil) != c.wantErr {
			t.Errorf("exit %s: got %v, %v", c.exit, got, err)
		}
	}
}

func TestExists(t *testing.T) {
	stubSystemctl(t)
	for exit, want := range map[string]bool{"0": true, "3": true, "4": false} {
		t.Setenv("STUB_EXIT", exit)
		if got, err := Exists("go-zenon"); got != want || err != nil {
			t.Errorf("exit %s: got %v, %v", exit, got, err)
		}
	}
	t.Setenv("STUB_EXIT", "1")
	if _, err := Exists("go-zenon"); err == nil {
		t.Error("unexpected exit code must be an error")
	}
}

func TestStopAbortsOnUnknownState(t *testing.T) {
	stubSystemctl(t)
	t.Setenv("STUB_EXIT", "1")
	if err := Stop("go-zenon"); err == nil {
		t.Error("Stop must fail when the state cannot be determined")
	}
	t.Setenv("STUB_EXIT", "3")
	if err := Stop("go-zenon"); err != nil {
		t.Errorf("stopping an inactive unit is a no-op: %v", err)
	}
}

func TestStartMissingUnit(t *testing.T) {
	stubSystemctl(t)
	t.Setenv("STUB_EXIT", "4")
	if err := Start("go-zenon"); !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

func TestAvailable(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if err := Available(); err == nil {
		t.Error("missing systemctl must be reported")
	}
}

func TestInterrupted(t *testing.T) {
	execx.Configure(false, nil)
	if !interrupted(execx.New("sh", "-c", "kill -INT $$").Interactive()) {
		t.Error("SIGINT termination should count as interrupted")
	}
	if !interrupted(execx.New("sh", "-c", "exit 130").Interactive()) {
		t.Error("exit 130 should count as interrupted")
	}
	if interrupted(execx.New("sh", "-c", "exit 1").Interactive()) {
		t.Error("exit 1 is a failure")
	}
	if interrupted(execx.New("definitely-missing-binary-xyz").Interactive()) {
		t.Error("missing binary is a failure")
	}
}
