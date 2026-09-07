package alerts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/0x3639/nomctl/internal/alertproto"
	"github.com/0x3639/nomctl/internal/metrics"
)

// ReminderEvery is how often a still-firing alert is re-sent.
const ReminderEvery = 10 * time.Minute

// errorLogEvery bounds how often relay failures are logged.
const errorLogEvery = 10 * time.Minute

// AlertState is the daemon's memory of one alert. Acked is false while the
// relay has not yet accepted the current Firing value; the daemon keeps
// retrying on every step until it has.
type AlertState struct {
	Firing   bool      `json:"firing"`
	Acked    bool      `json:"acked"`
	Since    time.Time `json:"since,omitempty"`
	LastSent time.Time `json:"last_sent,omitempty"`
	Detail   string    `json:"detail,omitempty"`
}

// State is what the daemon persists for `nomctl alerts status`.
type State struct {
	Started         time.Time             `json:"started"`
	LastSample      time.Time             `json:"last_sample"`
	LastHeartbeatOK time.Time             `json:"last_heartbeat_ok"`
	LastError       string                `json:"last_error,omitempty"`
	Unpaired        bool                  `json:"unpaired"`
	Alerts          map[string]AlertState `json:"alerts"`
}

// Sampler is the subset of metrics.Sampler the daemon needs.
type Sampler interface {
	Take(ctx context.Context) metrics.Sample
}

// pillarSetter is implemented by *metrics.Sampler to receive the pillar name.
type pillarSetter interface {
	SetPillarName(name string)
}

// Daemon evaluates rules over samples and reports transitions.
type Daemon struct {
	cfg       Config
	cfgPath   string
	statePath string
	sampler   Sampler
	client    *Client
	rules     []Rule
	now       func() time.Time

	history      []metrics.Sample
	state        State
	evaluated    bool
	lastErrorLog time.Time
}

// NewDaemon wires a daemon. cfgPath is re-read on reload.
func NewDaemon(cfg Config, cfgPath, statePath string, sampler Sampler, client *Client) *Daemon {
	if ps, ok := sampler.(pillarSetter); ok {
		ps.SetPillarName(cfg.PillarName)
	}
	return &Daemon{
		cfg:       cfg,
		cfgPath:   cfgPath,
		statePath: statePath,
		sampler:   sampler,
		client:    client,
		rules:     AllRules(),
		now:       time.Now,
		state:     State{Alerts: map[string]AlertState{}},
	}
}

// Run loops until ctx is done. A value on reload re-reads the config.
func (d *Daemon) Run(ctx context.Context, reload <-chan struct{}) error {
	d.state.Started = d.now()
	ticker := time.NewTicker(d.cfg.Interval)
	defer ticker.Stop()
	d.Step(ctx)
	for {
		select {
		case <-ctx.Done():
			d.writeState()
			return nil
		case <-reload:
			cfg, err := Load(d.cfgPath)
			if err != nil {
				slog.Error("reload failed; keeping previous config", "err", err)
				continue
			}
			if err := d.apply(cfg); err != nil {
				slog.Error("reload failed; keeping previous config", "err", err)
				continue
			}
			ticker.Reset(d.cfg.Interval)
			slog.Info("configuration reloaded")
		case <-ticker.C:
			d.Step(ctx)
		}
	}
}

// apply installs a new config, rebuilding the relay client when the
// credentials changed and clearing a stale unpaired state in that case.
func (d *Daemon) apply(cfg Config) error {
	if cfg.RelayURL != d.cfg.RelayURL || cfg.NodeID != d.cfg.NodeID || cfg.Secret != d.cfg.Secret {
		client, err := NewClient(cfg, d.client.Version)
		if err != nil {
			return err
		}
		client.Now = d.client.Now
		client.HTTP = d.client.HTTP
		d.client = client
		d.state.Unpaired = false
		d.state.LastError = ""
	}
	d.cfg = cfg
	if ps, ok := d.sampler.(pillarSetter); ok {
		ps.SetPillarName(cfg.PillarName)
	}
	return nil
}

// Step performs one sample, evaluation and report cycle.
func (d *Daemon) Step(ctx context.Context) {
	now := d.now()
	sample := d.sampler.Take(ctx)
	d.history = trimHistory(append(d.history, sample), now)
	d.state.LastSample = now

	if !d.evaluated {
		// First evaluation: establish the baseline silently and announce.
		for _, r := range d.rules {
			rc := d.cfg.Rules[r.Name()]
			if !rc.Enabled {
				continue
			}
			res := r.Evaluate(d.history, rc)
			d.state.Alerts[r.Name()] = AlertState{Firing: res.Firing, Acked: true, Since: now, Detail: res.Detail}
		}
		d.evaluated = true
		d.send(ctx, alertproto.AlertRequest{Alert: "started", State: alertproto.Info, Severity: alertproto.InfoSev,
			Title: "alerts started", Detail: fmt.Sprintf("nomctl %s watching %s every %s", d.client.Version, sample.Service.Unit, d.cfg.Interval), At: now})
	} else {
		d.evaluate(ctx, now)
	}

	d.heartbeat(ctx, now, sample)
	d.writeState()
}

func (d *Daemon) evaluate(ctx context.Context, now time.Time) {
	for _, r := range d.rules {
		name := r.Name()
		rc := d.cfg.Rules[name]
		if !rc.Enabled {
			delete(d.state.Alerts, name)
			continue
		}
		info, _ := alertproto.Lookup(name)
		res := r.Evaluate(d.history, rc)
		prev, known := d.state.Alerts[name]
		if !known {
			// Rule enabled after start: take its current value as baseline.
			prev = AlertState{Firing: res.Firing, Acked: true, Since: now}
		}
		if res.Firing != prev.Firing {
			prev = AlertState{Firing: res.Firing, Since: now}
		}
		if res.Firing {
			// Informational alerts are never reminded, so a changed detail
			// (a newer release than the one already announced) is a new event.
			if info.Severity == alertproto.InfoSev && prev.Acked && prev.Detail != "" && prev.Detail != res.Detail {
				prev.Acked = false
			}
			prev.Detail = res.Detail
		}
		remind := info.Severity != alertproto.InfoSev && (prev.LastSent.IsZero() || now.Sub(prev.LastSent) >= ReminderEvery)
		needSend := !prev.Acked || (res.Firing && remind)
		if needSend {
			req := alertproto.AlertRequest{Alert: name, State: alertproto.OK, Severity: info.Severity, Title: info.OKTitle, At: now}
			if res.Firing {
				req.State, req.Title, req.Detail = alertproto.Firing, info.Title, res.Detail
			}
			if d.send(ctx, req) {
				prev.Acked = true
				prev.LastSent = now
			}
		}
		d.state.Alerts[name] = prev
	}
}

// send posts an alert; returns true when the relay accepted it.
func (d *Daemon) send(ctx context.Context, a alertproto.AlertRequest) bool {
	if d.state.Unpaired {
		return false
	}
	if err := d.client.Alert(ctx, a); err != nil {
		d.noteError(err)
		return false
	}
	slog.Info("alert sent", "alert", a.Alert, "state", string(a.State))
	return true
}

func (d *Daemon) heartbeat(ctx context.Context, now time.Time, s metrics.Sample) {
	if d.state.Unpaired {
		return
	}
	var upd UpdateInfo
	if UpdateChecker != nil {
		upd = UpdateChecker() // served from the on-disk cache between checks
	}
	hb := alertproto.HeartbeatRequest{At: now, Summary: Summarize(s, now, d.client.Version, upd)}
	if err := d.client.Heartbeat(ctx, hb); err != nil {
		d.noteError(err)
		return
	}
	d.state.LastHeartbeatOK = now
	d.state.LastError = ""
}

// Summarize builds the heartbeat summary from a sample.
func Summarize(s metrics.Sample, now time.Time, nomctlVersion string, upd UpdateInfo) alertproto.Summary {
	n := s.Node
	sum := alertproto.Summary{
		State: n.StateText, Height: n.CurrentHeight, Peers: n.NumPeers, Restarts: s.Service.NRestarts,
		TargetHeight: n.TargetHeight, MomentumRate: n.MomentumsPerSec,
		NodeVersion: n.Version, NodeCommit: n.Commit, NomctlVersion: nomctlVersion,
		Load1: s.Host.Load1, MemFree: s.Host.MemAvailable, MemTotal: s.Host.MemTotal,
		DiskFree: s.Host.DataDirFree, DiskTotal: s.Host.DataDirTotal,
	}
	if n.ETAKnown {
		sum.ETASeconds = int64(n.ETA / time.Second)
	}
	if n.FrontierKnown {
		sum.Frontier = n.FrontierHeight
		sum.FrontierAgeSeconds = int64(n.FrontierAge / time.Second)
	} else if n.Reachable {
		sum.LedgerBusy = true
	}
	if s.Service.ActiveState == "active" && !s.Service.Since.IsZero() {
		sum.UptimeSeconds = int64(now.Sub(s.Service.Since) / time.Second)
	}
	if s.Process.Present {
		sum.CPUPercent = s.Process.CPUPercent
		sum.RSS = s.Process.RSS
	}
	if p := n.Pillar; p.Configured {
		sum.PillarName = p.Name
		if p.Found {
			sum.PillarRank, sum.PillarProduced, sum.PillarExpected = p.Rank, p.Produced, p.Expected
		} else {
			sum.PillarError = p.Error
		}
	}
	if upd.NomctlLatest != "" && NewerVersion(upd.NomctlLatest, upd.NomctlRunning) {
		sum.NomctlUpdate = upd.NomctlLatest
	}
	sum.NodeUpdate = upd.NodeBehind
	if !n.Reachable {
		sum.State = "rpc unreachable"
	}
	if s.Service.ActiveState != "active" {
		sum.State = "service " + s.Service.ActiveState
	}
	return sum
}

func (d *Daemon) noteError(err error) {
	if errors.Is(err, ErrUnpaired) {
		if !d.state.Unpaired {
			slog.Error("the relay no longer knows this node; run: sudo nomctl alerts setup")
		}
		d.state.Unpaired = true
		d.state.LastError = err.Error()
		return
	}
	d.state.LastError = err.Error()
	if d.now().Sub(d.lastErrorLog) >= errorLogEvery {
		slog.Warn("relay request failed", "err", err)
		d.lastErrorLog = d.now()
	}
}

// writeState persists the state file atomically; failures are logged once.
func (d *Daemon) writeState() {
	if d.statePath == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(d.statePath), 0o755); err != nil {
		return
	}
	data, err := json.MarshalIndent(d.state, "", "  ")
	if err != nil {
		return
	}
	tmp := d.statePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err == nil {
		_ = os.Rename(tmp, d.statePath)
	}
}

// LoadState reads the daemon's state file.
func LoadState(path string) (State, error) {
	var st State
	data, err := os.ReadFile(path)
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return st, err
	}
	if st.Alerts == nil {
		st.Alerts = map[string]AlertState{}
	}
	return st, nil
}
