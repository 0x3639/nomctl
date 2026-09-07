package deploy

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/0x3639/nomctl/internal/config"
)

func TestParseBranches(t *testing.T) {
	out := "abc\trefs/heads/zeta\ndef\trefs/heads/master\nghi\trefs/heads/alpha\n\n"
	got := ParseBranches(out)
	want := []string{"master", "alpha", "zeta"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseBranches = %v, want %v", got, want)
	}
	if got := ParseBranches(""); len(got) != 0 {
		t.Errorf("empty input should yield no branches, got %v", got)
	}
}

func TestParseGoVersion(t *testing.T) {
	if v := ParseGoVersion("go version go1.23.0 linux/amd64"); v != "go1.23.0" {
		t.Errorf("got %q", v)
	}
	if v := ParseGoVersion("garbage"); v != "" {
		t.Errorf("got %q", v)
	}
}

func TestUnitFile(t *testing.T) {
	cfg := config.Default()
	unit := UnitFile(cfg)
	for _, want := range []string{
		"Description=znnd service",
		`ExecStart=:"/usr/local/bin/znnd" --data "/root/.znn"`,
		"KillMode=control-group",
		"LimitNOFILE=32768",
		"SuccessExitStatus=SIGKILL 9",
		"WantedBy=multi-user.target",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("unit missing %q:\n%s", want, unit)
		}
	}
	if strings.Contains(unit, "pkill") {
		t.Error("the unit must not kill every znnd on the host")
	}
}

func TestUnitFileQuotesConfiguredPaths(t *testing.T) {
	cfg := config.Default()
	cfg.InstallDir = `/opt/node binary/%i/$NODE`
	cfg.ZnnDir = "/srv/node data/\"quoted\"/back\\slash/%n/$DATA\nnext"
	want := `ExecStart=:"/opt/node binary/%%i/$NODE/znnd" --data "/srv/node data/\"quoted\"/back\\slash/%%n/$DATA\nnext"`
	var got string
	for _, line := range strings.Split(UnitFile(cfg), "\n") {
		if strings.HasPrefix(line, "ExecStart=:") {
			got = line
		}
	}
	if got != want {
		t.Errorf("ExecStart = %q; want %q", got, want)
	}
}

func TestCloneAndBuildKeepsServiceRunningUntilBuildSucceeds(t *testing.T) {
	for _, phase := range []string{"clone", "build", "stop", "success"} {
		t.Run(phase, func(t *testing.T) {
			cfg := config.Default()
			root := t.TempDir()
			cfg.WorkDir = filepath.Join(root, "work")
			cfg.InstallDir = filepath.Join(root, "bin")
			toolsDir := filepath.Join(root, "tools")
			for _, dir := range []string{cfg.WorkDir, cfg.InstallDir, toolsDir, filepath.Dir(cfg.GoBinary())} {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(cfg.BinaryPath(), []byte("original"), 0o755); err != nil {
				t.Fatal(err)
			}
			calls := filepath.Join(root, "calls")
			t.Setenv("CALLS", calls)
			t.Setenv("FAIL_PHASE", phase)
			t.Setenv("STOPPED", filepath.Join(root, "stopped"))
			scripts := map[string]string{
				filepath.Join(toolsDir, "git"): `#!/bin/sh
printf 'clone\n' >> "$CALLS"
if [ "$FAIL_PHASE" = clone ]; then exit 1; fi
mkdir -p "$5/build"
`,
				cfg.GoBinary(): `#!/bin/sh
printf 'build\n' >> "$CALLS"
if [ "$FAIL_PHASE" = build ]; then exit 1; fi
printf 'replacement' > "$3"
`,
				filepath.Join(toolsDir, "systemctl"): `#!/bin/sh
case "$1" in
  show)
    printf 'LoadState=loaded\n'
    if [ -f "$STOPPED" ]; then printf 'ActiveState=inactive\n'; else printf 'ActiveState=active\n'; fi
    ;;
  stop)
    printf 'stop\n' >> "$CALLS"
    if [ "$FAIL_PHASE" = stop ]; then exit 1; fi
    : > "$STOPPED"
    ;;
  *) exit 1 ;;
esac
`,
			}
			for path, body := range scripts {
				if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("PATH", toolsDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			err := CloneAndBuild(cfg, "https://example.invalid/node.git", "main")
			if (err != nil) != (phase != "success") {
				t.Fatalf("phase %s: %v", phase, err)
			}
			got, readErr := os.ReadFile(cfg.BinaryPath())
			if readErr != nil {
				t.Fatal(readErr)
			}
			wantBinary := "original"
			if phase == "success" {
				wantBinary = "replacement"
			}
			if string(got) != wantBinary {
				t.Errorf("installed binary = %q; want %q", got, wantBinary)
			}
			gotCalls, readErr := os.ReadFile(calls)
			if readErr != nil {
				t.Fatal(readErr)
			}
			wantCalls := "clone\n"
			if phase != "clone" {
				wantCalls += "build\n"
			}
			if phase == "stop" || phase == "success" {
				wantCalls += "stop\n"
			}
			if string(gotCalls) != wantCalls {
				t.Errorf("calls = %q; want %q", gotCalls, wantCalls)
			}
		})
	}
}
