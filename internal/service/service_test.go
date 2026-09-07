package service

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0x3639/nomctl/internal/execx"
)

// stubSystemctl supplies explicit systemd states without touching host units.
func stubSystemctl(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	t.Setenv("STUB_CALLS", calls)
	t.Setenv("STUB_STOPPED", filepath.Join(dir, "stopped"))
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$STUB_CALLS"
case "$1" in
  show)
    if [ "${STUB_SHOW_EXIT:-0}" != 0 ]; then exit "$STUB_SHOW_EXIT"; fi
    printf 'LoadState=%s\n' "${STUB_LOAD:-loaded}"
    state="${STUB_STATE:-active}"
    if [ -f "$STUB_STOPPED" ]; then state="${STUB_AFTER_STOP:-inactive}"; fi
    printf 'ActiveState=%s\n' "$state"
    ;;
  stop)
    if [ "${STUB_STOP_EXIT:-0}" != 0 ]; then exit "$STUB_STOP_EXIT"; fi
    : > "$STUB_STOPPED"
    ;;
  *) exit "${STUB_EXIT:-0}" ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "systemctl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	execx.Configure(false, nil)
	return calls
}

func TestStatus(t *testing.T) {
	stubSystemctl(t)
	for _, want := range []State{Active, Inactive, Failed, Activating, Deactivating, Reloading, Maintenance, Refreshing} {
		t.Run(want.String(), func(t *testing.T) {
			t.Setenv("STUB_STATE", want.String())
			got, err := Status("go-zenon")
			if got != want || err != nil {
				t.Errorf("Status = %v, %v; want %v", got, err, want)
			}
		})
	}
	t.Setenv("STUB_LOAD", "not-found")
	t.Setenv("STUB_STATE", "inactive")
	if got, err := Status("go-zenon"); got != NotFound || err != nil {
		t.Errorf("missing unit: %v, %v", got, err)
	}
	// A running unit can outlive its unit file; its active state still matters.
	t.Setenv("STUB_STATE", "active")
	if got, err := Status("go-zenon"); got != Active || err != nil {
		t.Errorf("running unit without unit file: %v, %v", got, err)
	}
	t.Setenv("STUB_STATE", "unexpected")
	if got, err := Status("go-zenon"); got != Unknown || err == nil {
		t.Errorf("unrecognized state: %v, %v", got, err)
	}
	t.Setenv("STUB_SHOW_EXIT", "1")
	if got, err := Status("go-zenon"); got != Unknown || err == nil {
		t.Errorf("query failure: %v, %v", got, err)
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

func TestStopWaitsForTerminalState(t *testing.T) {
	for _, state := range []State{Active, Inactive, Failed, Activating, Deactivating, Reloading, Maintenance, Refreshing} {
		t.Run(state.String(), func(t *testing.T) {
			calls := stubSystemctl(t)
			t.Setenv("STUB_STATE", state.String())
			if err := Stop("go-zenon"); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(calls)
			if err != nil {
				t.Fatal(err)
			}
			want := "show --property=LoadState --property=ActiveState go-zenon\nstop go-zenon\nshow --property=LoadState --property=ActiveState go-zenon\n"
			if string(got) != want {
				t.Errorf("calls = %q, want %q", got, want)
			}
		})
	}
}

func TestStopRefusesUncertainResult(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"query failure":      {"STUB_SHOW_EXIT": "1"},
		"unknown state":      {"STUB_STATE": "unrecognized"},
		"stop failure":       {"STUB_STOP_EXIT": "1"},
		"still activating":   {"STUB_AFTER_STOP": "activating"},
		"still deactivating": {"STUB_AFTER_STOP": "deactivating"},
		"still active":       {"STUB_AFTER_STOP": "active"},
	} {
		t.Run(name, func(t *testing.T) {
			stubSystemctl(t)
			for key, value := range env {
				t.Setenv(key, value)
			}
			if err := Stop("go-zenon"); err == nil {
				t.Fatal("Stop accepted an uncertain result")
			}
		})
	}
}

func TestStopMissingUnit(t *testing.T) {
	calls := stubSystemctl(t)
	t.Setenv("STUB_LOAD", "not-found")
	t.Setenv("STUB_STATE", "inactive")
	if err := Stop("go-zenon"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "stop go-zenon") {
		t.Fatal("missing unit must not receive a stop job")
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
