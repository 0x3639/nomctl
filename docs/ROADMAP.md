# nomctl roadmap

Features planned beyond v0.2.0, in priority order. Each one gets a design
spec under `docs/superpowers/specs/` before code. Inspiration for several
items is [MyTonCtrl](https://github.com/ton-blockchain/mytonctrl), the TON
node controller; the mapping is noted where it applies.

Status legend: **planned** (agreed, not designed), **designing** (spec in
progress), **in progress** (plan being executed), **done** (released).

## v0.3.0

### 1. Alerting — done (v0.3.0)

Notify the operator when the node needs attention, and again when it
recovers. Signals come from the existing metrics sampler:

- service down / crash loop (restart count climbing)
- sync stalled (synced but frontier momentum older than 2 minutes) and
  sync fell behind (target minus current growing)
- not enough peers
- data or backup disk below the free-space minimum
- memory near the host total, open files near the limit
- pillar missed momentums (once item 2 lands)
- backup timer failed or has not run within its cadence

Delivery: one shared Telegram bot behind a relay (`nomctl-relay`, a
container with SQLite, hosted on Coolify) so operators never create a bot;
pairing by code. Each alert has an "ok" counterpart, a 10 minute reminder,
per-alert enable/disable and thresholds, and Telegram-side mutes. The
relay raises `node_silent` when heartbeats stop. Spec:
`docs/superpowers/specs/2026-09-06-alerting-design.md`. A generic webhook
output remains a possible follow-up.

MyTonCtrl equivalent: `setup_alert_bot`, `list_alerts`, `enable_alert`,
`disable_alert`, `test_alert`.

### 2. Pillar awareness — done (v0.4.0)

The alerts node name doubles as the pillar name (validated with
`embedded.pillar.getByName`, overridable with `nomctl alerts set
pillar.name`). `status` and `top` show rank and produced vs expected
momentums; `pillar_missed` alerts on missed production and
`momentums_stalled` on a node whose frontier stops moving regardless of
sync state. Spec: `docs/superpowers/specs/2026-09-06-pillar-alerts-design.md`.
A `nomctl pillars` listing is still open.

MyTonCtrl equivalent: validator section of `status`, `vl`.

### 3. Self-update — done (v0.5.0)

`nomctl upgrade`: download the latest GitHub release for the host
architecture, verify `checksums.txt`, replace the binary atomically (same
logic as `install.sh`), print the changelog. `nomctl status` shows "nomctl
update available" and, by comparing the running znnd commit with the
upstream branch head, "node update available"; `nomctl deploy` then
rebuilds.

MyTonCtrl equivalent: `update`, `upgrade`.

## v0.4.0

### 4. Wallet and config backup — planned

`nomctl backup --wallet`: a separate small archive of `wallet/` and
`config.json`, optionally encrypted with a passphrase (age or AES-GCM),
kept apart from the chain-data archives and never pruned by the retention
rule. `nomctl restore --wallet` counterpart. The current backup covers chain
data only, which is replaceable; the wallet is not.

MyTonCtrl equivalent: `create_backup`, `restore_backup`.

### 5. Config editor — planned

`nomctl config show|get|set`: read and change `config.json` keys with
validation, a timestamped copy of the previous file, and a restart prompt.
Initial keys: RPC enable/bind/ports, log level, min/max peers, seeders,
producing address. nomctl does not touch `config.json` today.

MyTonCtrl equivalent: `installer` sub-commands, `set`/`get`.

### 6. Network check — planned

`nomctl net check`: confirm port 35995/TCP is reachable from outside (via a
self-connect through the public IP, with a helper endpoint if one becomes
available), count inbound vs outbound peers, and print firewall guidance.
The number one cause of `not enough peers`.

MyTonCtrl equivalent: `checkAdnl`, `adnl_connection_failed` alert.

### 7. Metrics exporter — planned

`nomctl exporter`: a Prometheus `/metrics` endpoint on localhost fed by the
sampler (sync state, heights, rate, peers, restarts, RSS, open files, pillar
stats), registered as a scrape job in the Prometheus that `analytics install`
sets up, plus a nomctl Grafana dashboard that uses it.

MyTonCtrl equivalent: `prometheus_url`.

## Later

### 8. Benchmark — planned

`nomctl benchmark`: sequential and random IO on the data directory, memory
bandwidth and CPU, compared with node requirements, for sizing a VPS before
deploy.

MyTonCtrl equivalent: `benchmark`.

### 9. Doctor — planned

`nomctl doctor`: run the sampler and the crash-marker grep, match against
the README "common signatures" table and print the diagnosis and suggested
fix. Turns the troubleshooting section into a command.

### 12. Orchestrator — done (v0.7.0)

`nomctl orchestrator {status,logs,hard-reset}` and an Orchestrator submenu.
Hard reset ports the script: stop the unit, wait 10 s, delete `queues/` and
`events/` under `/root/.orchestrator`, start. Every function refuses on a
node without the unit. Further orchestrator functions are added to this
submenu as they are needed.

### 10. Bootstrap sync — done (v0.6.0)

`nomctl bootstrap [URL] [--discard]`: download the snapshot zip and its
`.hash` sidecar, verify the SHA-256, inspect the layout
(`backup/{nom,network,consensus}.bak/`), extract into staging, stop the
service, keep the previous data under the restore directory (or delete it
with `--discard`), rename into place, start. Default URL baked in and
overridable with `NOMCTL_BOOTSTRAP_URL`; the menu asks for the URL, whether
to keep the old data, and confirms.

### 11. Analytics dashboard refresh — planned

Improve the embedded Grafana dashboard once the exporter exists: sync rate,
peers, restarts, pillar stats next to the node_exporter panels.

## Deliberately not planned

- **Modes** (liteserver, validator, pools): one node type in Zenon.
- **Remote controller**: manage many nodes from one console; ssh and the
  non-interactive commands cover this.
- **Fleet telemetry**: no central Zenon endpoint, and a privacy cost.
- **Account inspection, bookmarks, governance voting**: `znn-cli` territory.

## Smaller items

- Make the RPC endpoint configurable (`NOMCTL_RPC_URL`) for nodes that
  moved the port.
- znn-sdk-go cannot be used until it builds with CGO disabled (go-zenon's
  VM pulls in go-ethereum's cgo secp256k1); `internal/node` mirrors its
  field names so a later switch is mechanical.
- Context/timeouts for commands run during support-bundle collection.
- `nomctl logs --since` and `--grep` passthroughs to journalctl.
