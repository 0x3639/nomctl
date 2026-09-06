package deploy

import (
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
		"ExecStart=/usr/local/bin/znnd",
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
