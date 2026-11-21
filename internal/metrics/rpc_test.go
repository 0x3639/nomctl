package metrics

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeRPCConfig(t *testing.T, dir, endpoint string) {
	t.Helper()
	u, err := url.Parse(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"RPC":{"HTTPHost":%q,"HTTPPort":%s},"Producer":{"Password":"private-password"}}`, u.Hostname(), u.Port())
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestSamplerUsesConfiguredHTTP(t *testing.T) {
	srv := fakeNode(t, 1000)
	defer srv.Close()
	dir := t.TempDir()
	writeRPCConfig(t, dir, srv.URL)
	s := testSampler(t, "")
	s.Node, s.dataDir = nil, dir
	now := time.Unix(1700000000, 0)
	s.Now = func() time.Time { return now }
	smp := s.Take(t.Context())
	if !smp.Node.Reachable || smp.Node.URL != srv.URL || smp.Node.CurrentHeight != 1000 {
		t.Fatalf("configured HTTP listener not sampled: %+v", smp.Node)
	}
	// Follow config edits on the next sample, without mixing rate history.
	next := fakeNode(t, 2000)
	defer next.Close()
	writeRPCConfig(t, dir, next.URL)
	now = now.Add(time.Second)
	smp = s.Take(t.Context())
	if !smp.Node.Reachable || smp.Node.URL != next.URL || smp.Node.CurrentHeight != 2000 || smp.Node.MomentumsPerSec != 0 {
		t.Fatalf("new endpoint or rate incorrect: %+v", smp.Node)
	}
	for _, body := range []string{`{"RPC":{"EnableHTTP":false}}`, `{"RPC":{"HTTPPort":"wrong"}}`, `{`} {
		if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		smp = s.Take(t.Context())
		if smp.Node.Reachable || smp.Node.URL != "" || smp.Node.Error == "" || len(s.heights) != 0 {
			t.Fatalf("invalid/disabled settings fell back to a listener: %+v", smp.Node)
		}
		text := Format(smp)
		if !strings.Contains(text, "node rpc check failed:") || !strings.Contains(text, "go-zenon active (running)") {
			t.Fatalf("settings failure hid service state: %s", text)
		}
	}
}

func TestSamplerUsesProbeRPCSettings(t *testing.T) {
	srv := fakeNode(t, 1000)
	defer srv.Close()
	dir := t.TempDir()
	writeRPCConfig(t, dir, srv.URL)
	now := time.Unix(1700000000, 0)
	p, err := TakeProbe("testdata/proc", 1234, dir, now)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(p)
	if err != nil || strings.Contains(string(raw), "private-password") || strings.Contains(string(raw), "Producer") {
		t.Fatalf("probe must contain connection settings only: %s, %v", raw, err)
	}
	// The daemon cannot read the node directory. Remove the config to ensure
	// the probe supplies the endpoint, rather than a direct read or default.
	if err := os.Remove(filepath.Join(dir, "config.json")); err != nil {
		t.Fatal(err)
	}
	s := testSampler(t, "")
	s.Node, s.dataDir = nil, dir
	s.Now = func() time.Time { return now }
	s.ProbePath = filepath.Join(t.TempDir(), "probe.json")
	if err := WriteProbe(s.ProbePath, p); err != nil {
		t.Fatal(err)
	}
	if n := s.Take(t.Context()).Node; !n.Reachable || n.URL != srv.URL {
		t.Fatalf("probe endpoint not used: %+v", n)
	}
	for _, kind := range []string{"stale", "wrong directory", "legacy", "invalid config", "disabled"} {
		t.Run(kind, func(t *testing.T) {
			bad := p
			switch kind {
			case "stale":
				bad.At = now.Add(-ProbeMaxAge - time.Second)
			case "wrong directory":
				bad.RPCDataDir = "/another-node"
			case "legacy":
				bad.RPCDataDir, bad.RPCURL = "", ""
			case "invalid config":
				bad.RPCError = "invalid RPC.HTTPPort"
			case "disabled":
				bad.RPCURL = ""
			}
			if err := WriteProbe(s.ProbePath, bad); err != nil {
				t.Fatal(err)
			}
			if n := s.Take(t.Context()).Node; n.Reachable || n.Error == "" || n.URL != "" {
				t.Fatalf("unusable probe must not contact a fallback listener: %+v", n)
			}
		})
	}
}
