package relay

import (
	"strings"
	"testing"
	"time"

	"github.com/0x3639/nomctl/internal/alertproto"
)

func TestNodeCardFull(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	n := Node{Name: "pillar-1", Host: "go-zenon-hot-fix", LastSeen: now.Add(-15 * time.Second), Summary: alertproto.Summary{
		State: "syncing", Height: 11107882, TargetHeight: 14135103, Peers: 28, Restarts: 0,
		MomentumRate: 6.2, ETASeconds: 5*86400 + 14*3600,
		Frontier: 11108343, FrontierAgeSeconds: 3, UptimeSeconds: 2*86400 + 4*3600,
		NodeVersion: "v0.0.7", NodeCommit: "a1b2c3d", NomctlVersion: "0.7.1",
		PillarName: "MyPillar", PillarRank: 11, PillarProduced: 118, PillarExpected: 121,
		Load1: 1.2, MemFree: 3328599654, MemTotal: 8375186227,
		DiskFree: 190 << 30, DiskTotal: 500 << 30, CPUPercent: 45, RSS: 2254857830,
		NomctlUpdate: "v0.7.2", NodeUpdate: true,
	}}
	got := NodeCard(n, now)
	for _, want := range []string{
		"🟢 *pillar\\-1* · go\\-zenon\\-hot\\-fix · seen 15s ago",
		"*Sync* syncing 11,107,882 / 14,135,103 \\(78\\.6%\\) · 6\\.2 mom/s · ETA 5d 14h",
		"*Frontier* 11,108,343 · 3s ago",
		"*Peers* 28 · restarts 0 · up 2d 4h",
		"*Pillar* MyPillar rank 11 · 118 / 121 produced this epoch",
		"*Node* znnd v0\\.0\\.7 \\(a1b2c3d\\) · nomctl 0\\.7\\.1",
		"*Host* load 1\\.2 · mem 3\\.1 GB / 7\\.8 GB free · disk 190\\.0 GB / 500\\.0 GB free \\(38%\\)",
		"*Process* cpu 45% · rss 2\\.1 GB",
		"*Update* nomctl 0\\.7\\.2 available · go\\-zenon has new commits",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestNodeCardOldNodeAndSilent(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	// A node on a release before the richer summary sends four fields.
	old := Node{Name: "old", Host: "old", LastSeen: now.Add(-12 * time.Minute), Silent: true,
		Summary: alertproto.Summary{State: "synced", Height: 5000, Peers: 9, Restarts: 2}}
	got := NodeCard(old, now)
	if !strings.HasPrefix(got, "🔴 *old* · silent · seen 12m 0s ago") {
		t.Errorf("header: %q", got)
	}
	if !strings.Contains(got, "*Sync* synced 5,000") || !strings.Contains(got, "*Peers* 9 · restarts 2") {
		t.Errorf("basic lines: %q", got)
	}
	for _, absent := range []string{"Frontier", "Pillar", "Node*", "Host", "Process", "Update"} {
		if strings.Contains(got, absent) {
			t.Errorf("%s should be absent for an old node: %q", absent, got)
		}
	}
	// No summary at all (paired, never heartbeated).
	if got := NodeCard(Node{Name: "new", LastSeen: now}, now); got != "🟢 *new* · seen 0s ago" {
		t.Errorf("bare: %q", got)
	}
	// Busy ledger and pillar not found.
	busy := Node{Name: "b", LastSeen: now, Summary: alertproto.Summary{State: "syncing", Height: 1, LedgerBusy: true, PillarName: "P", PillarError: "not found in the pillar list"}}
	got = NodeCard(busy, now)
	if !strings.Contains(got, "*Frontier* ledger busy") || !strings.Contains(got, "*Pillar* P: not found in the pillar list") {
		t.Errorf("busy/pillar error: %q", got)
	}
}

func TestCardHelpers(t *testing.T) {
	if commas(999) != "999" || commas(1000) != "1,000" || commas(14135103) != "14,135,103" {
		t.Error("commas")
	}
	if gb(512<<20) != "512 MB" || gb(3<<30) != "3.0 GB" || gb(2<<40) != "2.0 TB" {
		t.Errorf("gb: %s %s %s", gb(512<<20), gb(3<<30), gb(2<<40))
	}
	if humanAge(-5*time.Second) != "0s" || humanAge(90*time.Second) != "1m 30s" {
		t.Error("humanAge")
	}
}
