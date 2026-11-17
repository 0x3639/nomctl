package doctor

import (
	"context"

	"github.com/0x3639/nomctl/internal/metrics"
)

type observation struct {
	metrics.Sample
	// Empty means the service identity was unchanged around RPC collection.
	continuity string
}

// The shared sampler queries systemd before RPC. Bracket each RPC observation
// with a second service query so a restart within a sample cannot look like
// continuous progress across two samples of the same old PID.
func observe(ctx context.Context, take func(context.Context) metrics.Sample, reread func(context.Context, string) (metrics.ServiceProps, error)) observation {
	o := observation{Sample: take(ctx)}
	before := o.Service
	after, err := reread(ctx, before.Unit)
	if err != nil || after.ActiveState == "" {
		o.continuity = "unknown"
	} else {
		switch {
		case before.Error != "" && before.ActiveState == "", before.MainPID > 0 && (before.Since.IsZero() || after.ExecMainStart.IsZero()):
			o.continuity = "unknown"
		case before.MainPID != after.MainPID || !before.Since.Equal(after.ExecMainStart) || before.NRestarts != after.NRestarts || before.ActiveState != after.ActiveState:
			o.continuity = "changed"
		}
		// Display the most recent state even when continuity was lost. Do not
		// attach an RPC version from an ambiguous process to the new identity.
		o.Service = metrics.ServiceSample{
			Unit: before.Unit, Found: after.ActiveState != "" && (after.ActiveState != "inactive" || after.MainPID > 0 || after.ControlGroup != ""),
			ActiveState: after.ActiveState, SubState: after.SubState, Result: after.Result,
			MainPID: after.MainPID, NRestarts: after.NRestarts, Since: after.ExecMainStart,
		}
	}
	if o.continuity != "" {
		o.Node.Version, o.Node.Commit = "", ""
	}
	return o
}

func observationChecks(a, b observation, s settings, configErr error) []Check {
	checks := sampleChecks(a.Sample, b.Sample, s, configErr)
	if a.continuity == "" && b.continuity == "" {
		return checks
	}
	continuity := check("restarts", "unknown", "Service continuity could not be confirmed", "A service identity query before or after RPC collection was unavailable; heights are not compared across these observations.", "Repeat the check when systemd responds and the node is continuously running.")
	if a.continuity == "changed" || b.continuity == "changed" {
		continuity = check("restarts", "warning", "Service changed during RPC collection", "PID, start time, restart count, or active state changed within an observation; heights are not compared across these observations.", "Check for maintenance or restarts, then repeat after the node is continuously running.")
	}
	for i, c := range checks {
		switch {
		case c.ID == "restarts" && c.Severity != "warning":
			checks[i] = continuity
		case c.ID == "chain":
			checks[i] = check("chain", "unknown", "Chain progress comparison is incomplete", "Process continuity was not established around both RPC observations; their heights are not compared.", "Repeat after the service is continuously running and systemd can confirm its identity.")
		case c.ID == "service" && b.continuity == "unknown":
			checks[i] = check("service", "unknown", "Service state could not be confirmed around RPC collection", "The pre- or post-RPC service query was unavailable.", "Check systemctl status and repeat the diagnostic.")
		}
	}
	return checks
}
