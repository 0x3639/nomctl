package doctor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/0x3639/nomctl/internal/metrics"
)

func TestRestartWithinRPCSampleInvalidatesProgress(t *testing.T) {
	for _, tc := range []struct {
		name     string
		change   func(*metrics.ServiceProps)
		queryErr error
		severity string
	}{
		{"PID", func(p *metrics.ServiceProps) { p.MainPID++ }, nil, "warning"},
		{"start time", func(p *metrics.ServiceProps) { p.ExecMainStart = p.ExecMainStart.Add(time.Second) }, nil, "warning"},
		{"restart counter", func(p *metrics.ServiceProps) { p.NRestarts++ }, nil, "warning"},
		{"state", func(p *metrics.ServiceProps) { p.ActiveState = "deactivating" }, nil, "warning"},
		{"query failure", func(p *metrics.ServiceProps) {}, errors.New("unavailable"), "unknown"},
		{"missing identity", func(p *metrics.ServiceProps) { p.ExecMainStart = time.Time{} }, nil, "unknown"},
	} {
		for _, which := range []string{"first", "second"} {
			t.Run(tc.name+"/"+which, func(t *testing.T) {
				a, b := healthySamples()
				b.Node.CurrentHeight++
				b.Node.Version = "v0.0.10"
				b.Node.Commit = "new-process-commit"
				changed := b
				if which == "first" {
					changed = a
				}
				props := metrics.ServiceProps{ActiveState: changed.Service.ActiveState, MainPID: changed.Service.MainPID, ExecMainStart: changed.Service.Since, NRestarts: changed.Service.NRestarts}
				tc.change(&props)
				queried := false
				o := observe(context.Background(), func(context.Context) metrics.Sample { queried = true; return changed }, func(context.Context, string) (metrics.ServiceProps, error) {
					if !queried {
						t.Fatal("service recheck happened before sample")
					}
					return props, tc.queryErr
				})
				first, second := observation{Sample: a}, observation{Sample: b}
				if which == "first" {
					first = o
				} else {
					second = o
				}
				checks := observationChecks(first, second, settings{rpcEnabled: true, minPeers: 8, port: 35995}, nil)
				if findCheck(t, checks, "chain").Severity != "unknown" || findCheck(t, checks, "restarts").Severity != tc.severity {
					t.Fatal(checks)
				}
				if o.Node.Version != "" || o.Node.Commit != "" {
					t.Fatal("ambiguous version retained", o)
				}
			})
		}
	}
}

func TestStableObservationKeepsProgressAndIdentity(t *testing.T) {
	a, b := healthySamples()
	b.Node.CurrentHeight++
	b.Node.Version = "v0.0.10"
	b.Node.Commit = "abc"
	o := observe(context.Background(), func(context.Context) metrics.Sample { return b }, func(context.Context, string) (metrics.ServiceProps, error) {
		return metrics.ServiceProps{ActiveState: b.Service.ActiveState, MainPID: b.Service.MainPID, ExecMainStart: b.Service.Since, NRestarts: b.Service.NRestarts}, nil
	})
	checks := observationChecks(observation{Sample: a}, o, settings{rpcEnabled: true, minPeers: 8}, nil)
	if o.continuity != "" || o.Node.Version != "v0.0.10" || findCheck(t, checks, "chain").Severity != "ok" || findCheck(t, checks, "restarts").Severity != "ok" {
		t.Fatal(o, checks)
	}
}

func TestKnownRestartIncreaseSurvivesUncertainContinuity(t *testing.T) {
	for _, reason := range []string{"missing timestamp", "failed post-RPC query"} {
		t.Run(reason, func(t *testing.T) {
			a, b := healthySamples()
			a.Service.NRestarts = 2
			b.Service.NRestarts = 3
			var queryErr error
			if reason == "missing timestamp" {
				a.Service.Since = time.Time{}
			} else {
				queryErr = errors.New("systemd unavailable")
			}
			first := observe(context.Background(), func(context.Context) metrics.Sample { return a }, func(context.Context, string) (metrics.ServiceProps, error) {
				return metrics.ServiceProps{ActiveState: a.Service.ActiveState, MainPID: a.Service.MainPID, NRestarts: a.Service.NRestarts, ExecMainStart: a.Service.Since}, queryErr
			})
			checks := observationChecks(first, observation{Sample: b}, settings{rpcEnabled: true, minPeers: 8}, nil)
			if first.continuity != "unknown" || findCheck(t, checks, "chain").Severity != "unknown" || findCheck(t, checks, "restarts").Severity != "warning" {
				t.Fatal(first, checks)
			}
		})
	}
}
