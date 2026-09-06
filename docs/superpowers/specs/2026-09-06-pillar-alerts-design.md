# Pillar awareness and momentum alerts

Status: approved in conversation. Target release: v0.4.0.

## Goals

1. Alert when the node stops syncing momentums, regardless of what sync
   state it reports (covers the incident where go-zenon kept running but
   stopped advancing).
2. Alert when this node's pillar stops producing its expected momentums.
3. Show the pillar's rank and production in `nomctl status` and `nomctl top`.

## Non-goals

- Using znn-sdk-go: it cannot be built CGO-free (go-zenon's VM pulls in
  go-ethereum's cgo secp256k1), so `internal/node` keeps its own client.
  Field names mirror the SDK so a later switch is mechanical.
- Detecting a network-wide halt as distinct from a local one.

## Identifying the pillar

The node name given at `nomctl alerts setup` is the pillar name. Setup
looks it up with `embedded.pillar.getByName`; on a match it stores it as
`pillar_name` in `/etc/nomctl/alerts.json` and reports "monitoring pillar
NAME (rank N)". On no match it says so and continues as a plain node.
`nomctl alerts set pillar.name NAME` changes it later (empty clears it) and
is validated the same way. `status`/`top` read `pillar_name` from the
alerts config when present, else `NOMCTL_PILLAR_NAME`.

## RPC additions (`internal/node`)

- `embedded.pillar.getByName(name)` returns
  `{name, rank, type, ownerAddress, producerAddress, withdrawAddress,
  giveMomentumRewardPercentage, giveDelegateRewardPercentage, isRevocable,
  revokeCooldown, revokeTimestamp, currentStats{producedMomentums,
  expectedMomentums}, weight}`; `null` when unknown.

`node.Snapshot` gains `Pillar *PillarInfo` when a name is configured; a
pillar lookup failure does not fail the snapshot (it is recorded as
`PillarErr`).

## Sample additions (`internal/metrics`)

`NodeSample.Pillar` = `{Configured bool, Found bool, Name string, Rank int,
Produced, Expected uint64, Weight string}`.

`status` line: `Pillar    MyPillar rank 12, produced 118 / 121 expected this epoch`
(or `MyPillar: not found in the pillar list`). `top` shows the same in the
NODE panel.

## Rules (`internal/alerts`)

| Alert | Severity | Fires when | Thresholds |
|---|---|---|---|
| `momentums_stalled` | critical | service active, RPC reachable, and the frontier height is unchanged across `minutes` of samples (any sync state) | `minutes=5` |
| `pillar_missed` | critical | pillar configured and found; over the last `minutes`, expected momentums grew by at least `missed` more than produced did (epoch rollover, where expected decreases, resets the window) | `minutes=30 missed=2` |

Details: "height 1,234,567 unchanged for 5m 0s, frontier 6m 10s old" and
"MyPillar missed 2 of 5 expected momentums in the last 30m (118 / 121 this
epoch)". Both have `_ok` recoveries and follow the existing reminder rules.
`sync_stalled` stays as the fast path for a synced node.

## Testing

Rule tables for both (fires, not enough history, recovery, epoch rollover,
pillar not configured); node client test for `getByName` including a null
result; sampler test with a fake pillar response; Format/top rendering.
