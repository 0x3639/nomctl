package alerts

import (
	"fmt"
	"time"

	"github.com/0x3639/nomctl/internal/metrics"
	"github.com/0x3639/nomctl/internal/node"
)

// HistoryWindow is how much sample history the daemon keeps for rules.
const HistoryWindow = MaxWindowMinutes * time.Minute

// Result of evaluating a rule.
type Result struct {
	Firing bool
	Detail string
}

// Rule evaluates a condition over the sample history (oldest first).
type Rule interface {
	Name() string
	Evaluate(history []metrics.Sample, cfg RuleConfig) Result
}

// BackupInfo is what the backup_stale rule needs; the daemon supplies it.
type BackupInfo struct {
	TimerEnabled bool
	Newest       time.Time
	CadenceDays  int
}

// BackupChecker is consulted by the backup_stale rule. The daemon sets it;
// tests stub it. nil disables the rule.
var BackupChecker func() BackupInfo

// AllRules returns every node-side rule in spec order.
func AllRules() []Rule {
	return []Rule{
		ruleFunc{"service_down", serviceDown},
		ruleFunc{"crash_loop", crashLoop},
		ruleFunc{"sync_stalled", syncStalled},
		ruleFunc{"sync_behind", syncBehind},
		ruleFunc{"not_enough_peers", notEnoughPeers},
		ruleFunc{"disk_low", diskLow},
		ruleFunc{"memory_high", memoryHigh},
		ruleFunc{"fds_high", fdsHigh},
		ruleFunc{"backup_stale", backupStale},
		ruleFunc{"rpc_unreachable", rpcUnreachable},
	}
}

type ruleFunc struct {
	name string
	fn   func(history []metrics.Sample, cfg RuleConfig) Result
}

func (r ruleFunc) Name() string { return r.name }
func (r ruleFunc) Evaluate(history []metrics.Sample, cfg RuleConfig) Result {
	return r.fn(history, cfg)
}

// --- history helpers -------------------------------------------------------

// trimHistory drops samples older than HistoryWindow before now.
func trimHistory(h []metrics.Sample, now time.Time) []metrics.Sample {
	cut := now.Add(-HistoryWindow)
	i := 0
	for i < len(h) && h[i].Taken.Before(cut) {
		i++
	}
	return h[i:]
}

// lastN returns the newest n samples (fewer if not available).
func lastN(h []metrics.Sample, n int) []metrics.Sample {
	if len(h) <= n {
		return h
	}
	return h[len(h)-n:]
}

// since returns the samples taken within d of the newest one.
func since(h []metrics.Sample, d time.Duration) []metrics.Sample {
	if len(h) == 0 {
		return nil
	}
	cut := h[len(h)-1].Taken.Add(-d)
	i := 0
	for i < len(h) && h[i].Taken.Before(cut) {
		i++
	}
	return h[i:]
}

// covers reports whether the window of samples spans at least d minus a
// tolerance of one sampling interval, so "for 5 minutes" needs 5 minutes
// of history, not just one recent sample.
func covers(h []metrics.Sample, d time.Duration) bool {
	if len(h) < 2 {
		return false
	}
	span := h[len(h)-1].Taken.Sub(h[0].Taken)
	interval := h[1].Taken.Sub(h[0].Taken)
	return span+interval >= d
}

func minutes(cfg RuleConfig, rule, key string) time.Duration {
	return time.Duration(cfg.Threshold(rule, key) * float64(time.Minute))
}

// --- rules -----------------------------------------------------------------

func serviceDown(h []metrics.Sample, _ RuleConfig) Result {
	last := lastN(h, 2)
	if len(last) < 2 {
		return Result{}
	}
	for _, s := range last {
		if s.Service.ActiveState == "active" {
			return Result{}
		}
	}
	s := last[len(last)-1].Service
	if !s.Found {
		return Result{Firing: true, Detail: fmt.Sprintf("%s: %s", s.Unit, s.Error)}
	}
	return Result{Firing: true, Detail: fmt.Sprintf("%s %s (%s), %d restarts", s.Unit, s.ActiveState, s.SubState, s.NRestarts)}
}

func crashLoop(h []metrics.Sample, cfg RuleConfig) Result {
	win := since(h, minutes(cfg, "crash_loop", "window_minutes"))
	if len(win) < 2 {
		return Result{}
	}
	first, last := win[0].Service.NRestarts, win[len(win)-1].Service.NRestarts
	count := int(cfg.Threshold("crash_loop", "count"))
	if last-first >= count {
		return Result{Firing: true, Detail: fmt.Sprintf("%d restarts in the last %s (total %d)", last-first, minutes(cfg, "crash_loop", "window_minutes"), last)}
	}
	return Result{}
}

func syncStalled(h []metrics.Sample, _ RuleConfig) Result {
	last := lastN(h, 2)
	if len(last) < 2 {
		return Result{}
	}
	for _, s := range last {
		if !s.Node.Reachable || !s.Node.Stalled {
			return Result{}
		}
	}
	n := last[len(last)-1].Node
	return Result{Firing: true, Detail: fmt.Sprintf("synced at height %s but frontier is %s old", metrics.Commas(n.CurrentHeight), metrics.HumanDuration(n.FrontierAge))}
}

func syncBehind(h []metrics.Sample, cfg RuleConfig) Result {
	d := minutes(cfg, "sync_behind", "minutes")
	win := since(h, d)
	if !covers(win, d) {
		return Result{}
	}
	for _, s := range win {
		if !s.Node.Reachable || s.Node.State != node.Syncing {
			return Result{}
		}
	}
	first, last := win[0].Node, win[len(win)-1].Node
	gapFirst := int64(first.TargetHeight) - int64(first.CurrentHeight) //nolint:gosec // heights fit
	gapLast := int64(last.TargetHeight) - int64(last.CurrentHeight)    //nolint:gosec // heights fit
	if gapLast >= gapFirst && gapLast > 0 {
		return Result{Firing: true, Detail: fmt.Sprintf("%s momentums behind, gap not closing for %s (%.1f mom/s)", metrics.Commas(uint64(gapLast)), metrics.HumanDuration(d), last.MomentumsPerSec)} //nolint:gosec // positive
	}
	return Result{}
}

func notEnoughPeers(h []metrics.Sample, cfg RuleConfig) Result {
	d := minutes(cfg, "not_enough_peers", "minutes")
	minPeers := int(cfg.Threshold("not_enough_peers", "min_peers"))
	win := since(h, d)
	if !covers(win, d) {
		return Result{}
	}
	for _, s := range win {
		if !s.Node.Reachable {
			return Result{}
		}
		if s.Node.State != node.NotEnoughPeers && s.Node.NumPeers >= minPeers {
			return Result{}
		}
	}
	n := win[len(win)-1].Node
	if n.State == node.NotEnoughPeers {
		return Result{Firing: true, Detail: fmt.Sprintf("node reports 'not enough peers' (%d connected) for %s; check that port 35995/TCP is reachable", n.NumPeers, metrics.HumanDuration(d))}
	}
	return Result{Firing: true, Detail: fmt.Sprintf("%d peers connected (minimum %d) for %s; check that port 35995/TCP is reachable", n.NumPeers, minPeers, metrics.HumanDuration(d))}
}

func diskLow(h []metrics.Sample, cfg RuleConfig) Result {
	if len(h) == 0 {
		return Result{}
	}
	host := h[len(h)-1].Host
	minFree := uint64(cfg.Threshold("disk_low", "min_free_gb") * (1 << 30))
	if host.DataDirTotal > 0 && host.DataDirFree < minFree {
		return Result{Firing: true, Detail: fmt.Sprintf("%s has %s free (minimum %s)", host.DataDir, metrics.HumanBytes(host.DataDirFree), metrics.HumanBytes(minFree))}
	}
	return Result{}
}

func memoryHigh(h []metrics.Sample, cfg RuleConfig) Result {
	if len(h) == 0 {
		return Result{}
	}
	s := h[len(h)-1]
	if !s.Process.Present || s.Host.MemTotal == 0 {
		return Result{}
	}
	pct := float64(s.Process.RSS) / float64(s.Host.MemTotal) * 100
	if pct > cfg.Threshold("memory_high", "pct") {
		return Result{Firing: true, Detail: fmt.Sprintf("znnd uses %s, %.0f%% of %s", metrics.HumanBytes(s.Process.RSS), pct, metrics.HumanBytes(s.Host.MemTotal))}
	}
	return Result{}
}

func fdsHigh(h []metrics.Sample, cfg RuleConfig) Result {
	if len(h) == 0 {
		return Result{}
	}
	p := h[len(h)-1].Process
	if !p.Present || p.FDLimit == 0 {
		return Result{}
	}
	pct := float64(p.OpenFDs) / float64(p.FDLimit) * 100
	if pct > cfg.Threshold("fds_high", "pct") {
		return Result{Firing: true, Detail: fmt.Sprintf("%d of %d open files (%.0f%%)", p.OpenFDs, p.FDLimit, pct)}
	}
	return Result{}
}

func backupStale(h []metrics.Sample, _ RuleConfig) Result {
	if BackupChecker == nil || len(h) == 0 {
		return Result{}
	}
	info := BackupChecker()
	if !info.TimerEnabled {
		return Result{}
	}
	now := h[len(h)-1].Taken
	limit := time.Duration(info.CadenceDays+1) * 24 * time.Hour
	if info.Newest.IsZero() || now.Sub(info.Newest) > limit {
		age := "never"
		if !info.Newest.IsZero() {
			age = metrics.HumanDuration(now.Sub(info.Newest)) + " ago"
		}
		return Result{Firing: true, Detail: fmt.Sprintf("newest backup %s, expected every %d day(s)", age, info.CadenceDays)}
	}
	return Result{}
}

func rpcUnreachable(h []metrics.Sample, cfg RuleConfig) Result {
	d := minutes(cfg, "rpc_unreachable", "minutes")
	win := since(h, d)
	if !covers(win, d) {
		return Result{}
	}
	for _, s := range win {
		if s.Service.ActiveState != "active" || s.Node.Reachable {
			return Result{}
		}
	}
	n := win[len(win)-1].Node
	return Result{Firing: true, Detail: fmt.Sprintf("service active but %s not answering for %s: %s", n.URL, metrics.HumanDuration(d), n.Error)}
}
