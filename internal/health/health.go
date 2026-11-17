// Package health observes a node after a data or executable change. It never
// starts services, changes data, or performs automatic rollback.
package health

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/doctor"
	"github.com/0x3639/nomctl/internal/node"
)

const DefaultWindow = 30 * time.Second

type Report struct {
	Started        time.Time                `json:"started"`
	Finished       time.Time                `json:"finished"`
	Outcome        string                   `json:"outcome"`
	Reasons        []string                 `json:"reasons"`
	Before         doctor.NodeObservation   `json:"before"`
	After          []doctor.NodeObservation `json:"after"`
	ExpectedSHA256 string                   `json:"expected_sha256"`
}

func (r Report) Passed() bool { return r.Outcome == "healthy" || r.Outcome == "syncing" }

// Failure carries the exact matching recovery point to interactive callers.
type Failure struct {
	Report     Report
	RecoveryID string
	DataOnly   bool
	Cause      error
}

func (e *Failure) Error() string {
	text := "post-change health: " + e.Report.Outcome
	if len(e.Report.Reasons) > 0 {
		text += "; " + strings.Join(e.Report.Reasons, "; ")
	}
	if e.Cause != nil {
		text += "; " + e.Cause.Error()
	}
	if e.RecoveryID != "" {
		text += fmt.Sprintf("; retained recovery point %s; review 'nomctl restore point %s%s' before recovery", e.RecoveryID, e.RecoveryID, map[bool]string{true: " --data-only", false: ""}[e.DataOnly])
	}
	return text
}
func (e *Failure) Unwrap() error { return e.Cause }

func Observe(ctx context.Context, cfg config.Config) doctor.NodeObservation {
	return doctor.ObserveNode(ctx, cfg)
}

func Check(ctx context.Context, cfg config.Config, before doctor.NodeObservation, expected string, window time.Duration) (Report, error) {
	return check(ctx, before, expected, window, 5*time.Second, func(ctx context.Context) doctor.NodeObservation { return Observe(ctx, cfg) })
}

func check(ctx context.Context, before doctor.NodeObservation, expected string, window, interval time.Duration, take func(context.Context) doctor.NodeObservation) (Report, error) {
	if window < time.Second || window > 5*time.Minute {
		return Report{}, fmt.Errorf("health observation must be between 1s and 5m")
	}
	r := Report{Started: time.Now().UTC(), Before: before, ExpectedSHA256: expected, After: []doctor.NodeObservation{}}
	parent := ctx
	ctx, stop := context.WithTimeout(ctx, window)
	defer stop()
	for ctx.Err() == nil {
		r.After = append(r.After, take(ctx))
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
	r.Finished = time.Now().UTC()
	r.Outcome, r.Reasons = Evaluate(r.After, expected)
	if parent.Err() != nil {
		r.Outcome, r.Reasons = "unknown", []string{"health observation was interrupted"}
		return r, &Failure{Report: r, Cause: parent.Err()}
	}
	if !r.Passed() {
		return r, &Failure{Report: r}
	}
	return r, nil
}

// Evaluate compares progress only between observations of one incarnation.
// Pre-change heights are evidence, never a post-change progress baseline.
func Evaluate(samples []doctor.NodeObservation, expected string) (string, []string) {
	unknown := func(s string) (string, []string) { return "unknown", []string{s} }
	failed := func(s string) (string, []string) { return "failed", []string{s} }
	if len(samples) < 2 {
		return unknown("not enough post-change observations")
	}
	last := samples[len(samples)-1]
	s := last.Sample.Service
	n := last.Sample.Node
	if s.ActiveState == "failed" || s.ActiveState == "inactive" {
		return failed("node service is not running")
	}
	if last.Problem != "" || s.Error != "" || s.ActiveState != "active" || s.MainPID <= 0 || s.Since.IsZero() {
		return unknown("service identity could not be established around RPC")
	}
	if !n.Reachable {
		return failed("node RPC did not become responsive within the observation window")
	}
	if expected == "" || last.Identity.Running.SHA256 == "" || last.Identity.Installed.SHA256 == "" {
		return unknown("installed or running znnd checksum is unavailable")
	}
	if last.Identity.Running.SHA256 != expected || last.Identity.Installed.SHA256 != expected {
		return failed("running or installed znnd does not match the intended executable")
	}
	if n.Version == "" || n.Commit == "" {
		return unknown("RPC znnd version or commit is unavailable")
	}
	if n.NumPeers < 1 || n.State == node.NotEnoughPeers {
		return failed("node has no usable peers")
	}
	var first *doctor.NodeObservation
	for i := range samples {
		o := &samples[i]
		svc := o.Sample.Service
		if svc.MainPID > 0 && (svc.MainPID != s.MainPID || !svc.Since.Equal(s.Since) || svc.NRestarts != s.NRestarts) {
			return failed("node restarted during post-change observation")
		}
		if o.Problem == "changed" {
			return failed("service changed during RPC collection")
		}
		if o.Problem == "" && o.Sample.Node.Reachable && svc.MainPID == s.MainPID && !svc.Since.IsZero() && first == nil {
			first = o
		}
	}
	if first == nil || first == &samples[len(samples)-1] {
		return unknown("RPC has not supplied two stable observations yet")
	}
	if n.CurrentHeight < first.Sample.Node.CurrentHeight || n.FrontierKnown && first.Sample.Node.FrontierKnown && n.FrontierHeight < first.Sample.Node.FrontierHeight {
		return failed("chain height moved backwards within one process")
	}
	progress := n.CurrentHeight > first.Sample.Node.CurrentHeight || n.FrontierKnown && first.Sample.Node.FrontierKnown && n.FrontierHeight > first.Sample.Node.FrontierHeight
	if !progress {
		return "stalled", []string{"no chain progress observed during the bounded check; inspect logs and recheck"}
	}
	if n.State == node.Syncing {
		return "syncing", []string{"RPC responds, peers are connected, and chain height is advancing; catching up continues"}
	}
	if n.State != node.Done || !n.FrontierKnown {
		return unknown("sync state or ledger frontier is unavailable")
	}
	if n.Stalled || n.FrontierAge > 2*time.Minute {
		return "stalled", []string{"frontier remains old despite reported progress"}
	}
	return "healthy", []string{"service, RPC, peers, progress and intended znnd identity are confirmed"}
}

func Write(w io.Writer, r Report) {
	fmt.Fprintf(w, "Node health after change: %s\n", r.Outcome)
	for _, reason := range r.Reasons {
		fmt.Fprintln(w, "  "+reason)
	}
	show := func(label string, o doctor.NodeObservation) {
		n := o.Sample.Node
		fmt.Fprintf(w, "%s: service=%s, height=%d/%d, peers=%d, restarts=%d\n  znnd version=%s commit=%s\n  installed SHA-256=%s\n  running SHA-256=%s\n", label, o.Sample.Service.ActiveState, n.CurrentHeight, n.TargetHeight, n.NumPeers, o.Sample.Service.NRestarts, o.Identity.Version, o.Identity.Commit, o.Identity.Installed.SHA256, o.Identity.Running.SHA256)
	}
	show("Before", r.Before)
	if len(r.After) > 0 {
		show("After", r.After[len(r.After)-1])
	}
}
