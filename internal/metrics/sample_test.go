package metrics

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/node"
)

func fakeNode(t *testing.T, height uint64) *httptest.Server {
	t.Helper()
	h := strconv.FormatUint(height, 10)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Method string }
		_ = json.NewDecoder(r.Body).Decode(&req)
		var res string
		switch req.Method {
		case "stats.syncInfo":
			res = `{"state":1,"currentHeight":` + h + `,"targetHeight":2000}`
		case "stats.networkInfo":
			res = `{"numPeers":14,"peers":[],"self":null}`
		case "stats.processInfo":
			res = `{"version":"v0.0.7","commit":"a1b2c3d"}`
		case "stats.osInfo":
			res = `{"numGoroutine":42,"numCPU":4}`
		case "ledger.getFrontierMomentum":
			res = `{"height":` + h + `,"timestamp":1700000000,"hash":"h"}`
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":` + res + `}`))
	}))
}

func testSampler(t *testing.T, url string) *Sampler {
	t.Helper()
	cfg := config.Default()
	cfg.ZnnDir = "testdata"
	s := NewSampler(cfg)
	s.ProcRoot = "testdata/proc"
	s.CgroupRoot = "testdata/cgroup"
	s.Node = node.New(url)
	s.readProps = func(string) (ServiceProps, error) {
		return ServiceProps{ActiveState: "active", SubState: "running", MainPID: 1234, NRestarts: 1,
			ControlGroup: "/system.slice/go-zenon.service", ExecMainStart: time.Unix(1700000000, 0)}, nil
	}
	s.diskFree = func(string) (uint64, uint64, error) { return 210 << 30, 500 << 30, nil }
	return s
}

func TestTakeAndCPU(t *testing.T) {
	srv := fakeNode(t, 1000)
	defer srv.Close()
	s := testSampler(t, srv.URL)
	now := time.Unix(1700000100, 0)
	s.Now = func() time.Time { return now }

	first := s.Take(context.Background())
	if !first.Service.Found || first.Service.MainPID != 1234 || !first.Process.Present {
		t.Fatalf("service/process not sampled: %+v", first)
	}
	if first.Process.CPUPercent != 0 {
		t.Error("first sample has no CPU baseline, must be 0")
	}
	if first.Process.RSS != 1900000*1024 || first.Process.OpenFDs != 3 || first.Process.FDLimit != 32768 || first.Process.Cgroup.PidsCurrent != 38 {
		t.Errorf("process = %+v", first.Process)
	}
	if first.Host.Load1 != 1.2 || first.Host.DataDirFree != 210<<30 || first.Host.Pressure.IO != 15.4 {
		t.Errorf("host = %+v", first.Host)
	}
	if !first.Node.Reachable || first.Node.State != node.Syncing || first.Node.CurrentHeight != 1000 || first.Node.NumPeers != 14 || first.Node.Version != "v0.0.7" {
		t.Errorf("node = %+v", first.Node)
	}
	if first.Node.FrontierAge != 100*time.Second || first.Node.Stalled {
		t.Errorf("frontier age = %v stalled=%v", first.Node.FrontierAge, first.Node.Stalled)
	}

	now = now.Add(10 * time.Second)
	second := s.Take(context.Background())
	if second.Process.CPUPercent != 0 {
		t.Errorf("no tick change means 0%%, got %v", second.Process.CPUPercent)
	}
	// Simulate 250 ticks (2.5 s of CPU) over the 10 s since the previous sample.
	s.prevTicks -= 250
	now = now.Add(10 * time.Second)
	third := s.Take(context.Background())
	if third.Process.CPUPercent < 24.9 || third.Process.CPUPercent > 25.1 {
		t.Errorf("cpu = %v, want 25", third.Process.CPUPercent)
	}
}

func TestRateAndETA(t *testing.T) {
	base := time.Unix(1700000000, 0)
	pts := []heightPoint{{base, 1000}, {base.Add(10 * time.Second), 1050}, {base.Add(20 * time.Second), 1100}}
	if r := rate(pts); r < 4.99 || r > 5.01 {
		t.Errorf("rate = %v, want 5", r)
	}
	if rate(pts[:1]) != 0 {
		t.Error("one point has no rate")
	}
	srv := fakeNode(t, 1000)
	s := testSampler(t, srv.URL)
	now := base
	s.Now = func() time.Time { return now }
	s.Take(context.Background())
	srv.Close()
	srv2 := fakeNode(t, 1100)
	defer srv2.Close()
	s.Node = node.New(srv2.URL)
	now = now.Add(20 * time.Second)
	smp := s.Take(context.Background())
	if smp.Node.MomentumsPerSec < 4.99 || smp.Node.MomentumsPerSec > 5.01 {
		t.Errorf("mom/s = %v", smp.Node.MomentumsPerSec)
	}
	if !smp.Node.ETAKnown || smp.Node.ETA != 180*time.Second {
		t.Errorf("ETA = %v known=%v (900 left at 5/s)", smp.Node.ETA, smp.Node.ETAKnown)
	}
}

func TestStalledAndUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Method string }
		_ = json.NewDecoder(r.Body).Decode(&req)
		res := `{}`
		switch req.Method {
		case "stats.syncInfo":
			res = `{"state":2,"currentHeight":2000,"targetHeight":2000}`
		case "ledger.getFrontierMomentum":
			res = `{"height":2000,"timestamp":1700000000,"hash":"h"}`
		case "stats.networkInfo":
			res = `{"numPeers":1,"peers":[]}`
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":` + res + `}`))
	}))
	defer srv.Close()
	s := testSampler(t, srv.URL)
	s.Now = func() time.Time { return time.Unix(1700000000+200, 0) }
	smp := s.Take(context.Background())
	if !smp.Node.Stalled || smp.Node.StateText != "synced" || smp.Node.ETAKnown {
		t.Errorf("expected stalled synced node: %+v", smp.Node)
	}

	s.Node = node.New("http://127.0.0.1:1")
	down := s.Take(context.Background())
	if down.Node.Reachable || down.Node.Error == "" {
		t.Errorf("unreachable node not reported: %+v", down.Node)
	}
	text := Format(down)
	if !strings.Contains(text, "node rpc unreachable") || !strings.Contains(text, "go-zenon active (running)") {
		t.Errorf("Format:\n%s", text)
	}
}

func TestServiceMissing(t *testing.T) {
	srv := fakeNode(t, 1)
	defer srv.Close()
	s := testSampler(t, srv.URL)
	s.readProps = func(string) (ServiceProps, error) {
		return ServiceProps{ActiveState: "inactive", SubState: "dead"}, nil
	}
	smp := s.Take(context.Background())
	if smp.Service.Found || smp.Process.Present {
		t.Errorf("missing unit: %+v", smp.Service)
	}
	if text := Format(smp); !strings.Contains(text, "go-zenon: unit not found") || !strings.Contains(text, "Process   not running") {
		t.Errorf("Format:\n%s", text)
	}
}

func TestFormatHealthy(t *testing.T) {
	srv := fakeNode(t, 1000)
	defer srv.Close()
	s := testSampler(t, srv.URL)
	s.Now = func() time.Time { return time.Unix(1700000100, 0) }
	text := Format(s.Take(context.Background()))
	for _, want := range []string{"Service   go-zenon active (running), pid 1234, 1 restarts, up 1m 40s", "syncing 1,000 / 2,000 (50.0%)", "Peers     14 connected", "open files 3 / 32768", "Pressure  cpu 2.1%, io 15.4%, mem 0.0%", "Frontier  height 1,000, 1m 40s ago"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
}

func TestHelpers(t *testing.T) {
	if HumanBytes(1900<<20) != "1.9 GiB" || HumanBytes(512) != "512 B" || HumanBytes(1536) != "1.5 KiB" {
		t.Error("HumanBytes")
	}
	if HumanDuration(40*time.Hour) != "1d 16h" || HumanDuration(65*time.Second) != "1m 5s" || HumanDuration(3*time.Second) != "3s" || HumanDuration(-5*time.Second) != "0s" {
		t.Error("HumanDuration")
	}
	if Commas(1234567) != "1,234,567" || Commas(999) != "999" || Commas(1000) != "1,000" {
		t.Error("Commas")
	}
}
