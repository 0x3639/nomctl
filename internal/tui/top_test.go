package tui

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/0x3639/nomctl/internal/metrics"
	"github.com/0x3639/nomctl/internal/node"
)

var ansiRe = regexp.MustCompile("\x1b\\[[0-9;]*m")

func stripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

func fixedSample() metrics.Sample {
	taken := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	return metrics.Sample{
		Taken:   taken,
		Service: metrics.ServiceSample{Unit: "go-zenon", Found: true, ActiveState: "active", SubState: "running", MainPID: 1234, NRestarts: 0, Since: taken.Add(-26 * time.Hour)},
		Process: metrics.ProcessSample{Present: true, CPUPercent: 42, RSS: 1900 << 20, Threads: 38, OpenFDs: 412, FDLimit: 32768},
		Host:    metrics.HostSample{Load1: 1.2, Load5: 0.9, Load15: 0.8, MemTotal: 8 << 30, MemAvailable: 3 << 30, DataDir: "/root/.znn", DataDirFree: 210 << 30, DataDirTotal: 500 << 30, Pressure: metrics.Pressure{CPU: 2.1, IO: 15.4}},
		Node:    metrics.NodeSample{URL: node.DefaultURL, Reachable: true, State: node.Syncing, StateText: "syncing", CurrentHeight: 1234567, TargetHeight: 2000000, NumPeers: 14, Version: "v0.0.7", Commit: "a1b2c3d", FrontierHeight: 1234567, FrontierAge: 3 * time.Second, MomentumsPerSec: 5.2, ETA: 40 * time.Hour, ETAKnown: true},
	}
}

func TestRenderTop(t *testing.T) {
	out := stripANSI(renderTop(fixedSample(), []float64{1, 2, 3, 5.2}, 100, time.Date(2026, 9, 6, 12, 0, 1, 0, time.UTC)))
	t.Logf("\n%s", out)
	for _, want := range []string{"NODE", "PROCESS", "HOST", "syncing", "1,234,567", "2,000,000", "61.7%", "5.2 mom/s", "ETA 1d 16h", "14 peers", "42.0%", "1.9 GiB", "412 / 32768", "210.0 GiB", "q quit", "up 1d 2h"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	for _, line := range strings.Split(out, "\n") {
		if w := len([]rune(line)); w > 100 {
			t.Errorf("line wider than terminal (%d): %q", w, line)
		}
	}
}

func TestRenderTopPillar(t *testing.T) {
	s := fixedSample()
	s.Node.Pillar = metrics.PillarSample{Configured: true, Found: true, Name: "MyPillar", Rank: 11, Produced: 118, Expected: 121}
	out := stripANSI(renderTop(s, nil, 100, s.Taken))
	if !strings.Contains(out, "pillar MyPillar rank 11, produced 118 / 121 expected this epoch, 3 missed") {
		t.Errorf("pillar line missing:\n%s", out)
	}
}

func TestRenderTopUnreachableAndStalled(t *testing.T) {
	s := fixedSample()
	s.Node = metrics.NodeSample{URL: node.DefaultURL, Error: "connection refused"}
	out := stripANSI(renderTop(s, nil, 80, s.Taken))
	if !strings.Contains(out, "unreachable") || !strings.Contains(out, "connection refused") {
		t.Errorf("unreachable node not shown:\n%s", out)
	}
	s = fixedSample()
	s.Node.State, s.Node.StateText, s.Node.Stalled = node.Done, "synced", true
	s.Service.NRestarts = 3
	s.Process.Present = false
	out = stripANSI(renderTop(s, nil, 80, s.Taken))
	for _, want := range []string{"stalled", "3 restarts", "process not running"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestSparklineAndBar(t *testing.T) {
	if got := sparkline([]float64{0, 1, 2, 3, 4, 5, 6, 7}, 8); got != "▁▂▃▄▅▆▇█" {
		t.Errorf("sparkline = %q", got)
	}
	if got := sparkline(nil, 8); got != "" {
		t.Errorf("empty sparkline = %q", got)
	}
	if got := sparkline([]float64{1, 2, 3}, 2); len([]rune(got)) != 2 {
		t.Errorf("sparkline should keep the newest points: %q", got)
	}
	bar := stripANSI(progressBar(0.5, 10))
	if strings.Count(bar, "█") != 5 || strings.Count(bar, "░") != 5 {
		t.Errorf("bar = %q", bar)
	}
}

func TestTopModelUpdate(t *testing.T) {
	m := newTopModel(nil, time.Second)
	next, _ := m.Update(sampleMsg(fixedSample()))
	tm := next.(topModel)
	if tm.sample.Taken.IsZero() || len(tm.history) != 1 || tm.history[0] != 5.2 {
		t.Errorf("sample not stored: %+v", tm)
	}
	if !strings.Contains(stripANSI(tm.View()), "NODE") {
		t.Error("view should render the sample")
	}
}
