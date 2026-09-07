package metrics

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestProbeRoundTrip(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	p, err := TakeProbe("testdata/proc", 1234, now)
	if err != nil {
		t.Fatal(err)
	}
	if p.PID != 1234 || p.OpenFDs == 0 || p.FDLimit == 0 {
		t.Fatalf("probe = %+v", p)
	}
	path := filepath.Join(t.TempDir(), "probe.json")
	if err := WriteProbe(path, p); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o644 {
		t.Errorf("mode = %v", info.Mode())
	}
	got, err := ReadProbe(path, 1234, now.Add(time.Minute))
	if err != nil || got != p {
		t.Fatalf("read = %+v, %v", got, err)
	}
	if _, err := ReadProbe(path, 999, now); err == nil {
		t.Error("other pid accepted")
	}
	if _, err := ReadProbe(path, 1234, now.Add(ProbeMaxAge+time.Second)); err == nil {
		t.Error("stale probe accepted")
	}
	if _, err := ReadProbe(filepath.Join(t.TempDir(), "none"), 1234, now); err == nil {
		t.Error("missing probe accepted")
	}
	// A leftover symlink at the path is replaced, not written through.
	target := filepath.Join(t.TempDir(), "sentinel")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "probe.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := WriteProbe(link, p); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(target); string(got) != "keep" {
		t.Errorf("wrote through the link: %q", got)
	}
	if fi, _ := os.Lstat(link); fi.Mode()&os.ModeSymlink != 0 {
		t.Error("link not replaced")
	}
}

func TestSamplerFallsBackToProbe(t *testing.T) {
	// A proc tree whose fd directory is missing (as it looks to an
	// unprivileged reader) with a fresh probe next to it.
	srv := fakeNode(t, 1000)
	defer srv.Close()
	s := testSampler(t, srv.URL)
	root := t.TempDir()
	if err := os.CopyFS(root, os.DirFS("testdata/proc")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(root, "1234", "fd")); err != nil {
		t.Fatal(err)
	}
	s.ProcRoot = root
	now := time.Unix(1_800_000_000, 0)
	s.Now = func() time.Time { return now }
	probe := filepath.Join(t.TempDir(), "probe.json")
	s.ProbePath = probe
	if p := s.Take(t.Context()).Process; p.OpenFDs != 0 || p.FromProbe {
		t.Fatalf("without a probe: %+v", p)
	}
	if err := WriteProbe(probe, Probe{At: now, PID: 1234, OpenFDs: 777, FDLimit: 32768, ReadBytes: 5, WriteBytes: 6}); err != nil {
		t.Fatal(err)
	}
	p := s.Take(t.Context()).Process
	if !p.FromProbe || p.OpenFDs != 777 || p.FDLimit != 32768 {
		t.Fatalf("with a probe: %+v", p)
	}
	// Direct /proc access wins when available.
	s.ProcRoot = "testdata/proc"
	if p := s.Take(t.Context()).Process; p.FromProbe || p.OpenFDs == 777 {
		t.Fatalf("probe used although /proc is readable: %+v", p)
	}
}

func TestMountPoint(t *testing.T) {
	mi := filepath.Join(t.TempDir(), "mountinfo")
	content := "22 1 8:1 / / rw,relatime - ext4 /dev/sda1 rw\n" +
		"40 22 8:2 / /backup rw - ext4 /dev/sdb1 rw\n" +
		"41 22 8:3 / /root/.znn rw - ext4 /dev/sdc1 rw\n" +
		"42 22 8:4 / /mnt/with\\040space rw - ext4 /dev/sdd1 rw\n"
	if err := os.WriteFile(mi, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{
		"/root/.znn":         "/root/.znn",
		"/root/.znn/nom":     "/root/.znn",
		"/root/.znnd":        "/",
		"/root":              "/",
		"/backup/restore":    "/backup",
		"/mnt/with space/x":  "/mnt/with space",
		"/var/lib/something": "/",
	} {
		if got := MountPoint(mi, path); got != want {
			t.Errorf("MountPoint(%q) = %q, want %q", path, got, want)
		}
	}
	if got := MountPoint(filepath.Join(t.TempDir(), "missing"), "/root/.znn"); got != "/" {
		t.Errorf("missing mountinfo: %q", got)
	}
}
