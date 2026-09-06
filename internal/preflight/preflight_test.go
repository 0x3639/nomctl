package preflight

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPatchTimesyncd(t *testing.T) {
	cases := []struct {
		name, in, want string
		changed        bool
	}{
		{"empty", "", "[Time]\nNTP=time.cloudflare.com\n", true},
		{"already set", "[Time]\nNTP=time.cloudflare.com\n", "[Time]\nNTP=time.cloudflare.com\n", false},
		{"commented and other server", "[Time]\n#NTP=\nNTP=pool.ntp.org\nFallbackNTP=x\n", "[Time]\nNTP=time.cloudflare.com\n#NTP=\nFallbackNTP=x\n", true},
		{"no section", "[Other]\nfoo=bar\n", "[Other]\nfoo=bar\n\n[Time]\nNTP=time.cloudflare.com\n", true},
		{"case-insensitive", "[time]\nntp=Time.Cloudflare.Com\n", "[time]\nntp=Time.Cloudflare.Com\n", false},
	}
	for _, c := range cases {
		got, changed := PatchTimesyncd(c.in)
		if got != c.want || changed != c.changed {
			t.Errorf("%s: got %q (changed=%v), want %q (changed=%v)", c.name, got, changed, c.want, c.changed)
		}
	}
}

func TestReadMeminfo(t *testing.T) {
	p := filepath.Join(t.TempDir(), "meminfo")
	if err := os.WriteFile(p, []byte("MemTotal:       16000000 kB\nMemFree: 1 kB\nMemAvailable:    8000000 kB\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	total, avail, err := readMeminfo(p)
	if err != nil || total != 16000000 || avail != 8000000 {
		t.Errorf("got %d %d %v", total, avail, err)
	}
	if _, _, err := readMeminfo(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("expected error for missing file")
	}
}

func TestCPUMessage(t *testing.T) {
	err := CPU()
	if err != nil && !strings.Contains(err.Error(), "CPU cores") {
		t.Errorf("unexpected error %v", err)
	}
}
