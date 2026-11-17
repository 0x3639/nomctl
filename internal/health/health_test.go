package health

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/0x3639/nomctl/internal/doctor"
	"github.com/0x3639/nomctl/internal/metrics"
	"github.com/0x3639/nomctl/internal/node"
)

func healthy(height uint64) doctor.NodeObservation {
	return doctor.NodeObservation{Sample: metrics.Sample{
		Service: metrics.ServiceSample{ActiveState: "active", MainPID: 42, Since: time.Unix(123, 0)},
		Node:    metrics.NodeSample{Reachable: true, State: node.Done, CurrentHeight: height, FrontierHeight: height, FrontierKnown: true, NumPeers: 8, Version: "v1", Commit: "abc"},
	}, Identity: doctor.Identity{Installed: doctor.Digest{SHA256: "expected"}, Running: doctor.Digest{SHA256: "expected"}}}
}

func TestEvaluate(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		edit       func(*doctor.NodeObservation)
	}{
		{"healthy", "healthy", func(*doctor.NodeObservation) {}},
		{"catching up", "syncing", func(o *doctor.NodeObservation) {
			o.Sample.Node.State = node.Syncing
			o.Sample.Node.FrontierKnown = false
		}},
		{"stalled", "stalled", func(o *doctor.NodeObservation) { o.Sample.Node.CurrentHeight = 10; o.Sample.Node.FrontierHeight = 10 }},
		{"old frontier", "stalled", func(o *doctor.NodeObservation) { o.Sample.Node.FrontierAge = 5 * time.Minute }},
		{"down", "failed", func(o *doctor.NodeObservation) { o.Sample.Service.ActiveState = "failed" }},
		{"RPC failure", "failed", func(o *doctor.NodeObservation) { o.Sample.Node.Reachable = false }},
		{"no peers", "failed", func(o *doctor.NodeObservation) { o.Sample.Node.NumPeers = 0 }},
		{"restart", "failed", func(o *doctor.NodeObservation) { o.Sample.Service.NRestarts = 1 }},
		{"PID reused", "failed", func(o *doctor.NodeObservation) { o.Sample.Service.Since = time.Unix(124, 0) }},
		{"wrong binary", "failed", func(o *doctor.NodeObservation) { o.Identity.Running.SHA256 = "wrong" }},
		{"missing identity", "unknown", func(o *doctor.NodeObservation) { o.Identity.Installed.SHA256 = "" }},
		{"missing commit", "unknown", func(o *doctor.NodeObservation) { o.Sample.Node.Commit = "" }},
		{"uncertain service", "unknown", func(o *doctor.NodeObservation) { o.Problem = "unavailable" }},
		{"height decreased", "failed", func(o *doctor.NodeObservation) { o.Sample.Node.CurrentHeight = 9 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			after := healthy(11)
			tc.edit(&after)
			got, reasons := Evaluate([]doctor.NodeObservation{healthy(10), after}, "expected")
			if got != tc.want || len(reasons) == 0 {
				t.Fatalf("%s %v, want %s", got, reasons, tc.want)
			}
		})
	}
	if got, _ := Evaluate([]doctor.NodeObservation{healthy(10)}, "expected"); got != "unknown" {
		t.Fatal(got)
	}
}

func TestCancellationNeverPasses(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var n uint64
	r, err := check(ctx, healthy(999), "expected", time.Second, time.Millisecond, func(context.Context) doctor.NodeObservation {
		n++
		if n == 2 {
			cancel()
		}
		return healthy(n)
	})
	if r.Passed() || !errors.Is(err, context.Canceled) {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestRecoveryCommandPreservesDataOnly(t *testing.T) {
	e := &Failure{RecoveryID: "point", DataOnly: true, Report: Report{Outcome: "failed"}}
	if !strings.Contains(e.Error(), "'nomctl restore point point --data-only'") {
		t.Fatal(e.Error())
	}
}

func TestWindowExpiryDiscardsIncompleteSample(t *testing.T) {
	for _, complete := range []int{0, 1, 2} {
		t.Run(fmt.Sprint(complete), func(t *testing.T) {
			calls := 0
			r, err := check(context.Background(), healthy(999), "expected", time.Second, time.Millisecond, func(ctx context.Context) doctor.NodeObservation {
				calls++
				if calls <= complete {
					return healthy(uint64(calls))
				}
				<-ctx.Done()
				return doctor.NodeObservation{}
			})
			if len(r.After) != complete || r.Passed() != (complete == 2) || (err == nil) != r.Passed() {
				t.Fatalf("complete=%d: %+v, %v", complete, r, err)
			}
			if complete < 2 && r.Outcome != "unknown" {
				t.Fatalf("insufficient evidence must be unknown: %+v", r)
			}
		})
	}
}

func TestIntermediateHeightDecreaseFails(t *testing.T) {
	for _, frontier := range []bool{false, true} {
		samples := []doctor.NodeObservation{healthy(10), healthy(12), healthy(13)}
		if frontier {
			samples[2].Sample.Node.FrontierHeight = 11
		} else {
			samples[2].Sample.Node.CurrentHeight = 11
		}
		// Recovery above the earlier maximum must not conceal the regression.
		for range 2 {
			outcome, reasons := Evaluate(samples, "expected")
			if outcome != "failed" || !strings.Contains(strings.Join(reasons, " "), "backwards") {
				t.Fatalf("frontier=%t: %s %v", frontier, outcome, reasons)
			}
			samples = append(samples, healthy(14))
		}
	}
}
