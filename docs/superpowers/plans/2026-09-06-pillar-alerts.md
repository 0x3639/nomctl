# Pillar Alerts Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:executing-plans.

**Goal:** `momentums_stalled` and `pillar_missed` alerts plus pillar stats in status/top.
**Spec:** `docs/superpowers/specs/2026-09-06-pillar-alerts-design.md`

## Global constraints
Same as the alerting plan (GOWORK=off, lint clean, CGO off, commit trailer).

### Task 1: node client `PillarByName`
- `internal/node/client.go`: `type PillarStats {ProducedMomentums, ExpectedMomentums uint64}`, `type PillarInfo {Name string; Rank int; ProducerAddress, OwnerAddress string; CurrentStats *PillarStats; Weight string}`, `func (c *Client) PillarByName(ctx, name) (*PillarInfo, error)` (nil, nil when the RPC returns null), `Client.PillarName string`, `Snapshot.Pillar *PillarInfo`, `Snapshot.PillarErr error`.
- Test: fake server answers `embedded.pillar.getByName` with an object and with `null`.

### Task 2: sampler + format
- `metrics.NodeSample.Pillar PillarSample{Configured, Found, Name, Rank, Produced, Expected, Weight}`; `Sampler.PillarName`; `takeNode` fills it; `Format` prints the Pillar line; `top` NODE panel line.
- Tests: fake node with pillar response; Format contains "produced 118 / 121".

### Task 3: alert catalogue, config, rules
- `alertproto.Alerts` += `momentums_stalled`, `pillar_missed` (critical).
- `alerts.Config.PillarName string` (`json:"pillar_name"`); `Set("pillar.name", v)` special case (validation done by the command); thresholds `momentums_stalled.minutes=5`, `pillar_missed.minutes=30, missed=2`.
- Rules per spec; `pillar_missed` skips when `!Pillar.Configured || !Pillar.Found`.
- Tests: tables per spec.

### Task 4: daemon + commands
- Daemon passes `cfg.PillarName` to the sampler (`metrics.NewSampler(cfg)` then `s.PillarName = ...`), and on reload.
- `alerts setup`: after pairing, look up the name; set `PillarName`; print result. `alerts set pillar.name X`: validate via RPC (allow empty to clear), save, reload daemon. `alerts status`/`list` show the pillar.
- `cmd/status.go` and `cmd/top.go`: pillar name from alerts config or `NOMCTL_PILLAR_NAME` (add to `config.Vars`).
- Tests: command registration; config round trip with pillar name.

### Task 5: docs
- README alerts table + status example + pillar note; roadmap item 2 done.
