package tui

import (
	"testing"

	"github.com/0x3639/nomctl/internal/config"
)

func TestMenuOptions(t *testing.T) {
	opts := MenuOptions(config.Default())
	if len(opts) != 10 || opts[0].Value != "deploy" || opts[9].Value != "exit" {
		t.Errorf("unexpected options: %+v", opts)
	}
	if opts[1].Key != "restart → Restart the go-zenon service" {
		t.Errorf("label = %q", opts[1].Key)
	}
}

func TestParsers(t *testing.T) {
	for in, want := range map[string]int{"1": 1, "30": 30, "7": 7} {
		if n, ok := ParseMaxBackups(in); !ok || n != want {
			t.Errorf("ParseMaxBackups(%q) = %d,%v", in, n, ok)
		}
	}
	for _, in := range []string{"0", "31", "abc", "", "100"} {
		if _, ok := ParseMaxBackups(in); ok {
			t.Errorf("ParseMaxBackups(%q) should be invalid", in)
		}
	}
	if n, ok := ParseCadenceDays("365"); !ok || n != 365 {
		t.Error("365 days should be valid")
	}
	if _, ok := ParseCadenceDays("366"); ok {
		t.Error("366 days should be invalid")
	}
	for in, want := range map[string]int{"0": 0, "9": 9, "10": 10, "23": 23} {
		if n, ok := ParseHour(in); !ok || n != want {
			t.Errorf("ParseHour(%q) = %d,%v", in, n, ok)
		}
	}
	for _, in := range []string{"24", "-1", "x", ""} {
		if _, ok := ParseHour(in); ok {
			t.Errorf("ParseHour(%q) should be invalid", in)
		}
	}
}

func TestDispatchUnknown(t *testing.T) {
	cfg := config.Default()
	if err := Dispatch(&cfg, Action("bogus")); err == nil {
		t.Error("unknown action should error")
	}
}
