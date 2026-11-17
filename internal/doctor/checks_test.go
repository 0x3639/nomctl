package doctor

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/metrics"
	"github.com/0x3639/nomctl/internal/node"
)

func healthySamples() (metrics.Sample, metrics.Sample) {
	now := time.Date(2026, 11, 17, 1, 0, 0, 0, time.UTC)
	a := metrics.Sample{Taken: now, Service: metrics.ServiceSample{Found: true, Unit: "go-zenon", MainPID: 123, ActiveState: "active", Since: now.Add(-time.Hour)}, Node: metrics.NodeSample{Reachable: true, State: node.Done, CurrentHeight: 100, TargetHeight: 100, NumPeers: 12, FrontierKnown: true, FrontierHeight: 100, FrontierTime: now.Add(-2 * time.Second)}}
	b := a
	b.Taken = now.Add(10 * time.Second)
	return a, b
}

func findCheck(t *testing.T, checks []Check, id string) Check {
	t.Helper()
	for _, c := range checks {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("missing %s: %+v", id, checks)
	return Check{}
}

func TestHealthFindings(t *testing.T) {
	for _, tc := range []struct {
		name, id, severity string
		change             func(*metrics.Sample, *metrics.Sample)
	}{
		{"healthy", "service", "ok", func(a, b *metrics.Sample) {}},
		{"stopped", "service", "error", func(a, b *metrics.Sample) { b.Service.ActiveState = "inactive"; b.Service.MainPID = 0 }},
		{"missing", "service", "error", func(a, b *metrics.Sample) {
			b.Service.Found = false
			b.Service.ActiveState = "inactive"
			b.Service.Error = "unit not found"
		}},
		{"systemd unavailable", "service", "unknown", func(a, b *metrics.Sample) { b.Service = metrics.ServiceSample{Error: "unavailable"} }},
		{"rpc unavailable", "rpc", "error", func(a, b *metrics.Sample) { b.Node.Reachable = false }},
		{"rpc means peers unknown", "peers", "unknown", func(a, b *metrics.Sample) { b.Node.Reachable = false }},
		{"few peers", "peers", "warning", func(a, b *metrics.Sample) { b.Node.NumPeers = 2 }},
		{"invalid peer count", "peers", "unknown", func(a, b *metrics.Sample) { b.Node.NumPeers = -1 }},
		{"short unchanged synced", "chain", "ok", func(a, b *metrics.Sample) {}},
		{"syncing progress", "chain", "ok", func(a, b *metrics.Sample) { b.Node.State = node.Syncing; b.Node.CurrentHeight++ }},
		{"syncing unchanged", "chain", "warning", func(a, b *metrics.Sample) { b.Node.State = node.Syncing }},
		{"stale synced", "chain", "error", func(a, b *metrics.Sample) { b.Node.FrontierTime = b.Taken.Add(-3 * time.Minute) }},
		{"busy ledger", "chain", "unknown", func(a, b *metrics.Sample) { b.Node.FrontierKnown = false }},
		{"busy ledger progressing", "chain", "ok", func(a, b *metrics.Sample) { b.Node.FrontierKnown = false; b.Node.CurrentHeight++ }},
		{"height decreased", "chain", "warning", func(a, b *metrics.Sample) { b.Node.CurrentHeight-- }},
		{"clock mismatch", "chain", "warning", func(a, b *metrics.Sample) { b.Node.FrontierTime = b.Taken.Add(2 * time.Minute) }},
		{"missing baseline", "chain", "unknown", func(a, b *metrics.Sample) { a.Node.Reachable = false }},
		{"restarting", "restarts", "warning", func(a, b *metrics.Sample) { b.Service.NRestarts++ }},
		{"restarted chain", "chain", "unknown", func(a, b *metrics.Sample) { b.Service.NRestarts++; b.Node.CurrentHeight++ }},
		{"old restarts not crash loop", "restarts", "ok", func(a, b *metrics.Sample) { a.Service.NRestarts = 99; b.Service.NRestarts = 99 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, b := healthySamples()
			tc.change(&a, &b)
			c := findCheck(t, sampleChecks(a, b, settings{rpcEnabled: true, rpcURL: "http://127.0.0.1:35997", minPeers: 8, port: 35995}, nil), tc.id)
			if c.Severity != tc.severity {
				t.Fatalf("%+v", c)
			}
			if c.Evidence == "" || ((c.Severity == "warning" || c.Severity == "error" || c.Severity == "unknown") && c.NextStep == "") {
				t.Fatalf("incomplete finding: %+v", c)
			}
		})
	}
}

func TestUnavailableConfigurationAndDisabledRPC(t *testing.T) {
	a, b := healthySamples()
	a.Node.Reachable = false
	b.Node.Reachable = false
	checks := sampleChecks(a, b, settings{}, errors.New("password=never-print"))
	if findCheck(t, checks, "configuration").Severity != "error" || findCheck(t, checks, "rpc").Severity != "unknown" {
		t.Fatal(checks)
	}
	checks = sampleChecks(a, b, settings{}, nil)
	if findCheck(t, checks, "rpc").Severity != "info" {
		t.Fatal(checks)
	}
}

func TestDiskAndClockEvidence(t *testing.T) {
	for _, tc := range []struct {
		name string
		free int64
		used int
		err  error
		want string
	}{
		{"healthy", 20 << 20, 50, nil, "ok"}, {"low free", 1 << 20, 30, nil, "warning"}, {"full", 0, 100, nil, "warning"}, {"high usage", 20 << 20, 95, nil, "warning"}, {"unreadable", 0, 0, errors.New("missing"), "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := diskCheck("data_disk", "/data", 15<<20, func(string) (int64, int, error) { return tc.free, tc.used, tc.err })
			if c.Severity != tc.want {
				t.Fatal(c)
			}
		})
	}
	for _, tc := range []struct {
		synced bool
		offset time.Duration
		err    error
		want   string
	}{
		{true, time.Millisecond, nil, "ok"}, {false, 0, nil, "warning"}, {true, 2 * time.Second, nil, "warning"}, {true, -2 * time.Second, nil, "warning"}, {false, 0, errors.New("unavailable"), "unknown"},
	} {
		if c := clockCheck(tc.synced, tc.offset, tc.err); c.Severity != tc.want {
			t.Fatal(c)
		}
	}
}

func TestLogSignaturesDoNotDiscloseRawMessages(t *testing.T) {
	log := "too many open files password=secret-value\npanic: wallet private text\nno space left on device\ncorruption in db\nout of memory\n"
	checks := logChecks(log, nil)
	if len(checks) != 5 {
		t.Fatal(checks)
	}
	for _, c := range checks {
		if c.Severity != "warning" || strings.Contains(c.Evidence, "secret-value") || strings.Contains(c.Evidence, "private text") {
			t.Fatal(c)
		}
	}
	if logChecks("", nil)[0].Severity != "unknown" || logChecks("healthy message", nil)[0].Severity != "info" || logChecks(log, errors.New("failed"))[0].Severity != "unknown" {
		t.Fatal("journal completeness lost")
	}
}

func TestRunAlreadyCancelledAndInvalidWait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Run(ctx, config.Default(), Options{Wait: DefaultWait}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	for _, wait := range []time.Duration{0, -time.Second, 121 * time.Second} {
		if _, err := Run(context.Background(), config.Default(), Options{Wait: wait}); err == nil {
			t.Fatal("invalid wait accepted")
		}
	}
}
