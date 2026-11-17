package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

func TestMenuUpgradeRelaunchBoundaries(t *testing.T) {
	installFailure := errors.New("download or replacement failed")
	alertsFailure := errors.New("alerts daemon could not restart")
	execFailure := errors.New("permission denied")
	for _, tc := range []struct {
		name, target          string
		upgradeErr, execErr   error
		wantExec, wantWarning bool
	}{
		{"already current", "", nil, nil, false, false},
		{"cancelled", "", errCancelled, nil, false, false},
		{"install failed", "", installFailure, nil, false, false},
		{"installed", "/opt/custom nomctl", nil, nil, true, false},
		{"installed with alerts failure", "/opt/nomctl", alertsFailure, nil, true, true},
		{"relaunch failed", "/opt/nomctl", nil, execFailure, true, false},
		{"both post-install steps failed", "/opt/nomctl", alertsFailure, execFailure, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			args := []string{"--debug", "--log-file", "/tmp/log with spaces", "--skip-preflight"}
			env := []string{"PATH=/usr/bin", "NOMCTL_ZNN_DIR=/custom/data", "TERM=xterm-256color"}
			called, cleaned := false, false
			err := runMenuSelfUpgrade(&out, args, env, func() (string, error) {
				defer func() { cleaned = true }()
				return tc.target, tc.upgradeErr
			}, func(path string, argv, environment []string) error {
				called = true
				if !cleaned {
					t.Fatal("relaunched before updater cleanup")
				}
				if path != tc.target || !reflect.DeepEqual(argv, append([]string{tc.target}, args...)) || !reflect.DeepEqual(environment, env) {
					t.Fatal("launch path, flags or environment changed")
				}
				return tc.execErr
			})
			if called != tc.wantExec {
				t.Fatalf("relaunch=%v, want %v", called, tc.wantExec)
			}
			switch {
			case tc.execErr != nil:
				if !errors.Is(err, tc.execErr) || !strings.Contains(err.Error(), "exit and reopen") {
					t.Fatal(err)
				}
				if tc.upgradeErr != nil && !errors.Is(err, tc.upgradeErr) {
					t.Fatal("lost alerts failure", err)
				}
			case tc.target == "":
				if !errors.Is(err, tc.upgradeErr) {
					t.Fatal(err)
				}
			default:
				if err != nil {
					t.Fatal(err)
				}
			}
			if strings.Contains(out.String(), "Warning:") != tc.wantWarning {
				t.Fatal(out.String())
			}
			if !tc.wantExec && strings.Contains(out.String(), "Opening") {
				t.Fatal("announced a relaunch without installing")
			}
		})
	}
}

func TestSelfUpgradeInvalidFlagsNeverReturnInstalledPath(t *testing.T) {
	for _, tc := range []struct {
		check, rollback bool
		version         string
	}{
		{true, true, ""}, {false, true, "v0.16.0"}, {true, false, "v0.16.0"},
	} {
		target, err := runSelfUpgrade(io.Discard, tc.check, tc.rollback, tc.version)
		if err == nil || target != "" {
			t.Fatalf("target=%q error=%v", target, err)
		}
	}
}

func TestMenuRelaunchReplacesProcess(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	stage := filepath.Join(t.TempDir(), "upgrade-staging")
	if err := os.Mkdir(stage, 0700); err != nil {
		t.Fatal(err)
	}
	child := exec.Command(executable, "-test.run=^TestMenuReexecHelper$")
	child.Env = append(os.Environ(), "NOMCTL_MENU_REEXEC_TEST=before", "NOMCTL_TEST_STAGE="+stage)
	out, err := child.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(string(out), "updated process retained PID and environment; staging cleaned") {
		t.Fatal(string(out))
	}
}

func TestMenuReexecHelper(t *testing.T) {
	phase := os.Getenv("NOMCTL_MENU_REEXEC_TEST")
	if phase == "" {
		return
	}
	if phase == "after" {
		if strconv.Itoa(os.Getpid()) != os.Getenv("NOMCTL_TEST_PID") {
			os.Exit(21)
		}
		if _, err := os.Stat(os.Getenv("NOMCTL_TEST_STAGE")); !errors.Is(err, os.ErrNotExist) {
			os.Exit(22)
		}
		fmt.Println("updated process retained PID and environment; staging cleaned")
		os.Exit(0)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var env []string
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "NOMCTL_MENU_REEXEC_TEST=") {
			env = append(env, value)
		}
	}
	env = append(env, "NOMCTL_MENU_REEXEC_TEST=after", "NOMCTL_TEST_PID="+strconv.Itoa(os.Getpid()))
	err = runMenuSelfUpgrade(io.Discard, []string{"-test.run=^TestMenuReexecHelper$"}, env, func() (string, error) {
		defer func() { _ = os.RemoveAll(os.Getenv("NOMCTL_TEST_STAGE")) }()
		return executable, nil
	}, syscall.Exec)
	// Successful replacement must never return to the previous menu process.
	fmt.Fprintln(os.Stderr, "old process resumed:", err)
	os.Exit(23)
}
