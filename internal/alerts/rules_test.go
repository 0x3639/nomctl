package alerts

import (
	"strings"
	"testing"
	"time"

	"github.com/0x3639/nomctl/internal/metrics"
	"github.com/0x3639/nomctl/internal/node"
)

var base = time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)

// healthy returns a healthy sample taken at t.
func healthy(t time.Time) metrics.Sample {
	return metrics.Sample{
		Taken:   t,
		Service: metrics.ServiceSample{Unit: "go-zenon", Found: true, ActiveState: "active", SubState: "running", MainPID: 1, NRestarts: 0},
		Process: metrics.ProcessSample{Present: true, RSS: 2 << 30, OpenFDs: 400, FDLimit: 32768},
		Host:    metrics.HostSample{MemTotal: 8 << 30, MemAvailable: 4 << 30, DataDir: "/root/.znn", DataDirFree: 100 << 30, DataDirTotal: 500 << 30},
		Node:    metrics.NodeSample{URL: node.DefaultURL, Reachable: true, State: node.Done, StateText: "synced", CurrentHeight: 1000, TargetHeight: 1000, NumPeers: 14, FrontierAge: 5 * time.Second},
	}
}

// series builds n samples 30 s apart, applying mut to each with its index.
func series(n int, mut func(i int, s *metrics.Sample)) []metrics.Sample {
	var h []metrics.Sample
	for i := range n {
		s := healthy(base.Add(time.Duration(i) * 30 * time.Second))
		// A live chain adds about three momentums per 30 s sample.
		s.Node.FrontierHeight = uint64(1000 + 3*i)
		s.Node.CurrentHeight = s.Node.FrontierHeight
		s.Node.TargetHeight = s.Node.FrontierHeight
		if mut != nil {
			mut(i, &s)
		}
		h = append(h, s)
	}
	return h
}

func rule(t *testing.T, name string) Rule {
	t.Helper()
	for _, r := range AllRules() {
		if r.Name() == name {
			return r
		}
	}
	t.Fatalf("rule %s not found", name)
	return nil
}

func TestAllRulesMatchConfig(t *testing.T) {
	names := map[string]bool{}
	for _, r := range AllRules() {
		names[r.Name()] = true
	}
	for _, n := range RuleNames() {
		if !names[n] {
			t.Errorf("rule %s has config but no implementation", n)
		}
	}
	if len(names) != len(RuleNames()) {
		t.Errorf("%d rules vs %d configs", len(names), len(RuleNames()))
	}
}

func TestHealthyFiresNothing(t *testing.T) {
	BackupChecker = nil
	h := series(30, nil)
	cfg := DefaultConfig()
	for _, r := range AllRules() {
		if res := r.Evaluate(h, cfg.Rules[r.Name()]); res.Firing {
			t.Errorf("%s fired on healthy history: %s", r.Name(), res.Detail)
		}
	}
}

func TestServiceDown(t *testing.T) {
	r := rule(t, "service_down")
	cfg := DefaultConfig().Rules["service_down"]
	down := func(_ int, s *metrics.Sample) { s.Service.ActiveState, s.Service.SubState = "inactive", "dead" }
	if r.Evaluate(series(1, down), cfg).Firing {
		t.Error("one bad sample must not fire")
	}
	h := series(3, func(i int, s *metrics.Sample) {
		if i == 2 {
			down(i, s)
		}
	})
	if r.Evaluate(h, cfg).Firing {
		t.Error("only the newest sample down must not fire")
	}
	h = series(3, func(i int, s *metrics.Sample) {
		if i >= 1 {
			down(i, s)
		}
	})
	res := r.Evaluate(h, cfg)
	if !res.Firing || !strings.Contains(res.Detail, "inactive (dead)") {
		t.Errorf("two down samples: %+v", res)
	}
	h = series(3, func(i int, s *metrics.Sample) {
		if i >= 1 {
			s.Service.Found, s.Service.ActiveState, s.Service.Error = false, "", "unit not found"
		}
	})
	if res := r.Evaluate(h, cfg); !res.Firing || !strings.Contains(res.Detail, "unit not found") {
		t.Errorf("missing unit: %+v", res)
	}
}

func TestCrashLoop(t *testing.T) {
	r := rule(t, "crash_loop")
	cfg := DefaultConfig().Rules["crash_loop"]
	h := series(10, func(i int, s *metrics.Sample) { s.Service.NRestarts = i / 4 }) // 0,0,0,0,1,1,1,1,2,2
	if res := r.Evaluate(h, cfg); !res.Firing {
		t.Error("two restarts in 5 minutes must fire")
	}
	h = series(10, func(i int, s *metrics.Sample) {
		if i >= 5 {
			s.Service.NRestarts = 1
		}
	})
	if r.Evaluate(h, cfg).Firing {
		t.Error("one restart must not fire")
	}
	// Two restarts but 27 minutes apart: only one falls in the 10 minute window.
	h = series(60, func(i int, s *metrics.Sample) {
		if i >= 5 {
			s.Service.NRestarts = 1
		}
	})
	h[59].Service.NRestarts = 2
	if r.Evaluate(h, cfg).Firing {
		t.Error("restarts outside the window must not fire")
	}
}

func TestSyncStalled(t *testing.T) {
	r := rule(t, "sync_stalled")
	cfg := DefaultConfig().Rules["sync_stalled"]
	stalled := func(i int, s *metrics.Sample) { s.Node.Stalled, s.Node.FrontierAge = true, 3*time.Minute }
	if r.Evaluate(series(1, stalled), cfg).Firing {
		t.Error("one stalled sample must not fire")
	}
	if res := r.Evaluate(series(2, stalled), cfg); !res.Firing || !strings.Contains(res.Detail, "3m 0s old") {
		t.Errorf("two stalled samples: %+v", res)
	}
	h := series(2, stalled)
	h[1].Node.Reachable = false
	if r.Evaluate(h, cfg).Firing {
		t.Error("unreachable node is not stalled")
	}
}

func TestSyncBehind(t *testing.T) {
	r := rule(t, "sync_behind")
	cfg := DefaultConfig().Rules["sync_behind"]
	syncing := func(_ int, s *metrics.Sample) {
		s.Node.State, s.Node.StateText = node.Syncing, "syncing"
		s.Node.TargetHeight = 10000
		s.Node.CurrentHeight = 1000 // gap constant
	}
	if r.Evaluate(series(10, syncing), cfg).Firing {
		t.Error("less than 10 minutes of history must not fire")
	}
	if res := r.Evaluate(series(25, syncing), cfg); !res.Firing || !strings.Contains(res.Detail, "9,000 momentums behind") {
		t.Errorf("constant gap for 12 minutes: %+v", res)
	}
	progressing := func(i int, s *metrics.Sample) {
		syncing(i, s)
		s.Node.CurrentHeight = uint64(1000 + i*10)
	}
	if r.Evaluate(series(25, progressing), cfg).Firing {
		t.Error("closing gap must not fire")
	}
	if r.Evaluate(series(25, nil), cfg).Firing {
		t.Error("synced node must not fire")
	}
}

func TestNotEnoughPeers(t *testing.T) {
	r := rule(t, "not_enough_peers")
	cfg := DefaultConfig().Rules["not_enough_peers"]
	few := func(i int, s *metrics.Sample) { s.Node.NumPeers = 2 }
	if r.Evaluate(series(5, few), cfg).Firing {
		t.Error("2.5 minutes of few peers must not fire")
	}
	if res := r.Evaluate(series(12, few), cfg); !res.Firing || !strings.Contains(res.Detail, "2 peers") {
		t.Errorf("5.5 minutes of few peers: %+v", res)
	}
	state := func(i int, s *metrics.Sample) { s.Node.State = node.NotEnoughPeers }
	if !r.Evaluate(series(12, state), cfg).Firing {
		t.Error("NotEnoughPeers state must fire even with peers >= min")
	}
	h := series(12, few)
	h[6].Node.NumPeers = 10
	if r.Evaluate(h, cfg).Firing {
		t.Error("a healthy sample inside the window resets the condition")
	}
}

func TestThresholdRules(t *testing.T) {
	cfg := DefaultConfig()
	h := series(1, func(i int, s *metrics.Sample) { s.Host.DataDirFree = 10 << 30 })
	if res := rule(t, "disk_low").Evaluate(h, cfg.Rules["disk_low"]); !res.Firing || !strings.Contains(res.Detail, "10.0 GiB free") {
		t.Errorf("disk_low: %+v", res)
	}
	h = series(1, func(i int, s *metrics.Sample) { s.Host.DataDirTotal = 0 })
	if rule(t, "disk_low").Evaluate(h, cfg.Rules["disk_low"]).Firing {
		t.Error("unknown disk must not fire")
	}
	h = series(1, func(i int, s *metrics.Sample) { s.Process.RSS = 7 << 30 })
	if res := rule(t, "memory_high").Evaluate(h, cfg.Rules["memory_high"]); !res.Firing || !strings.Contains(res.Detail, "88%") {
		t.Errorf("memory_high: %+v", res)
	}
	h = series(1, func(i int, s *metrics.Sample) { s.Process.OpenFDs = 30000 })
	if res := rule(t, "fds_high").Evaluate(h, cfg.Rules["fds_high"]); !res.Firing || !strings.Contains(res.Detail, "30000 of 32768") {
		t.Errorf("fds_high: %+v", res)
	}
	rc := cfg.Rules["fds_high"]
	rc.Thresholds["pct"] = 95
	if rule(t, "fds_high").Evaluate(h, rc).Firing {
		t.Error("raised threshold must not fire")
	}
}

func TestRPCUnreachable(t *testing.T) {
	r := rule(t, "rpc_unreachable")
	cfg := DefaultConfig().Rules["rpc_unreachable"]
	down := func(_ int, s *metrics.Sample) { s.Node.Reachable, s.Node.Error = false, "connection refused" }
	if r.Evaluate(series(4, down), cfg).Firing {
		t.Error("2 minutes must not fire")
	}
	if res := r.Evaluate(series(12, down), cfg); !res.Firing || !strings.Contains(res.Detail, "connection refused") {
		t.Errorf("5.5 minutes: %+v", res)
	}
	h := series(12, func(i int, s *metrics.Sample) { down(i, s); s.Service.ActiveState = "inactive" })
	if r.Evaluate(h, cfg).Firing {
		t.Error("service down is not an rpc problem")
	}
}

func TestBackupStale(t *testing.T) {
	r := rule(t, "backup_stale")
	cfg := DefaultConfig().Rules["backup_stale"]
	h := series(1, nil)
	BackupChecker = nil
	if r.Evaluate(h, cfg).Firing {
		t.Error("no checker must not fire")
	}
	BackupChecker = func() BackupInfo { return BackupInfo{TimerEnabled: false} }
	if r.Evaluate(h, cfg).Firing {
		t.Error("timer disabled must not fire")
	}
	BackupChecker = func() BackupInfo {
		return BackupInfo{TimerEnabled: true, Newest: base.Add(-36 * time.Hour), CadenceDays: 1}
	}
	if r.Evaluate(h, cfg).Firing {
		t.Error("36h old with 1 day cadence (limit 2 days) must not fire")
	}
	BackupChecker = func() BackupInfo {
		return BackupInfo{TimerEnabled: true, Newest: base.Add(-50 * time.Hour), CadenceDays: 1}
	}
	if res := r.Evaluate(h, cfg); !res.Firing || !strings.Contains(res.Detail, "2d 2h ago") {
		t.Errorf("50h old: %+v", res)
	}
	BackupChecker = func() BackupInfo { return BackupInfo{TimerEnabled: true, CadenceDays: 7} }
	if res := r.Evaluate(h, cfg); !res.Firing || !strings.Contains(res.Detail, "never") {
		t.Errorf("never backed up: %+v", res)
	}
	BackupChecker = nil
}

func TestHistoryHelpers(t *testing.T) {
	h := series(100, nil) // 50 minutes
	trimmed := trimHistory(h, h[99].Taken)
	if len(trimmed) != 61 {
		t.Errorf("trimHistory kept %d, want 61 (30 minutes at 30s)", len(trimmed))
	}
	if got := since(h, 5*time.Minute); len(got) != 11 {
		t.Errorf("since 5m = %d samples", len(got))
	}
	if !covers(since(h, 5*time.Minute), 5*time.Minute) || covers(h[:2], 5*time.Minute) {
		t.Error("covers")
	}
	if len(lastN(h, 3)) != 3 || len(lastN(h[:1], 3)) != 1 {
		t.Error("lastN")
	}
}

func TestMomentumsStalled(t *testing.T) {
	r := rule(t, "momentums_stalled")
	cfg := DefaultConfig().Rules["momentums_stalled"]
	if r.Evaluate(series(20, nil), cfg).Firing {
		t.Error("advancing frontier must not fire")
	}
	stuck := func(_ int, s *metrics.Sample) {
		s.Node.FrontierHeight = 5000
		s.Node.State, s.Node.StateText = node.Syncing, "syncing"
	}
	if r.Evaluate(series(5, stuck), cfg).Firing {
		t.Error("needs 5 minutes of history")
	}
	res := r.Evaluate(series(12, stuck), cfg)
	if !res.Firing || !strings.Contains(res.Detail, "height 5,000 unchanged for 5m 0s") || !strings.Contains(res.Detail, "state: syncing") {
		t.Errorf("stuck while claiming to sync: %+v", res)
	}
	h := series(12, stuck)
	h[11].Node.FrontierHeight = 5001
	if r.Evaluate(h, cfg).Firing {
		t.Error("any movement in the window clears it")
	}
	h = series(12, stuck)
	h[6].Node.Reachable = false
	if r.Evaluate(h, cfg).Firing {
		t.Error("an unreachable sample is not evidence of a stall")
	}
	h = series(12, stuck)
	h[3].Service.ActiveState = "inactive"
	if r.Evaluate(h, cfg).Firing {
		t.Error("service down is service_down's job")
	}
}

func TestPillarMissed(t *testing.T) {
	r := rule(t, "pillar_missed")
	cfg := DefaultConfig().Rules["pillar_missed"]
	// 70 samples = 35 minutes; expected grows by 1 every 10 samples.
	producing := func(i int, s *metrics.Sample) {
		s.Node.Pillar = metrics.PillarSample{Configured: true, Found: true, Name: "P", Expected: uint64(100 + i/10), Produced: uint64(100 + i/10)}
	}
	if r.Evaluate(series(70, producing), cfg).Firing {
		t.Error("producing every expected momentum must not fire")
	}
	missing := func(i int, s *metrics.Sample) {
		s.Node.Pillar = metrics.PillarSample{Configured: true, Found: true, Name: "P", Expected: uint64(100 + i/10), Produced: 100}
	}
	if r.Evaluate(series(20, missing), cfg).Firing {
		t.Error("needs 30 minutes of history")
	}
	res := r.Evaluate(series(70, missing), cfg)
	if !res.Firing || !strings.Contains(res.Detail, "P missed") || !strings.Contains(res.Detail, "expected momentums in the last 30m") {
		t.Errorf("missing all slots: %+v", res)
	}
	one := func(i int, s *metrics.Sample) {
		exp := uint64(100 + i/10)
		prod := exp
		if i >= 60 {
			prod = exp - 1
		}
		s.Node.Pillar = metrics.PillarSample{Configured: true, Found: true, Name: "P", Expected: exp, Produced: prod}
	}
	if r.Evaluate(series(70, one), cfg).Firing {
		t.Error("one missed momentum is below the default threshold of 2")
	}
	// Epoch rollover: counters reset mid-window; only the post-reset part counts.
	rollover := func(i int, s *metrics.Sample) {
		if i < 40 {
			s.Node.Pillar = metrics.PillarSample{Configured: true, Found: true, Name: "P", Expected: uint64(500 + i/10), Produced: 490}
		} else {
			s.Node.Pillar = metrics.PillarSample{Configured: true, Found: true, Name: "P", Expected: uint64((i - 40) / 10), Produced: uint64((i - 40) / 10)}
		}
	}
	if r.Evaluate(series(70, rollover), cfg).Firing {
		t.Error("misses before the epoch rollover must not count")
	}
	// Two misses right after a rollover: not enough post-rollover history yet.
	freshMisses := func(i int, s *metrics.Sample) {
		if i < 60 {
			s.Node.Pillar = metrics.PillarSample{Configured: true, Found: true, Name: "P", Expected: uint64(500 + i/10), Produced: uint64(500 + i/10)}
		} else {
			s.Node.Pillar = metrics.PillarSample{Configured: true, Found: true, Name: "P", Expected: uint64((i - 60) / 4), Produced: 0}
		}
	}
	if r.Evaluate(series(70, freshMisses), cfg).Firing {
		t.Error("misses in a 5 minute post-rollover window must wait for a full window")
	}
	if !r.Evaluate(series(125, freshMisses), cfg).Firing {
		t.Error("once the post-rollover window spans 30 minutes the misses fire")
	}
	unconfigured := func(i int, s *metrics.Sample) { missing(i, s); s.Node.Pillar.Configured = false }
	if r.Evaluate(series(70, unconfigured), cfg).Firing {
		t.Error("no pillar configured means no alert")
	}
	notFound := func(i int, s *metrics.Sample) { missing(i, s); s.Node.Pillar.Found = false }
	if r.Evaluate(series(70, notFound), cfg).Firing {
		t.Error("unknown pillar means no alert")
	}
}

func TestUpdateAvailable(t *testing.T) {
	r := rule(t, "update_available")
	cfg := DefaultConfig()
	if cfg.Rules["update_available"].Enabled {
		t.Error("update_available must be opt-in")
	}
	rc := cfg.Rules["update_available"]
	h := series(1, nil)
	UpdateChecker = nil
	if r.Evaluate(h, rc).Firing {
		t.Error("no checker, no alert")
	}
	NewerVersion = func(latest, running string) bool { return latest == "v9.9.9" && running == "0.4.0" }
	defer func() { NewerVersion = func(string, string) bool { return false }; UpdateChecker = nil }()
	UpdateChecker = func() UpdateInfo { return UpdateInfo{NomctlLatest: "v0.4.0", NomctlRunning: "0.4.0"} }
	if r.Evaluate(h, rc).Firing {
		t.Error("up to date must not fire")
	}
	UpdateChecker = func() UpdateInfo {
		return UpdateInfo{NomctlLatest: "v9.9.9", NomctlRunning: "0.4.0", NodeBehind: true, NodeBranch: "master"}
	}
	res := r.Evaluate(h, rc)
	if !res.Firing || !strings.Contains(res.Detail, "nomctl 9.9.9 available") || !strings.Contains(res.Detail, "go-zenon master has new commits") {
		t.Errorf("update detail: %+v", res)
	}
}
