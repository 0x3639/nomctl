---
title: Roadmap
description: "Planned features, hardening follow-ups, and what is deliberately out of scope."
---

Planned features in priority order. Each gets a design spec in the repository under `docs/superpowers/specs/` before code. Several are inspired by [MyTonCtrl](https://github.com/ton-blockchain/mytonctrl), the TON node controller.

## Done

- **Alerting** (v0.3.0): Telegram through a shared relay, pairing by code, per-alert thresholds, mutes, `node_silent`.
- **Pillar awareness** (v0.4.0): the node name doubles as the pillar name; `status` and `top` show production; `pillar_missed` and `momentums_stalled` alerts.
- **Self-update** (v0.5.0): `nomctl upgrade` with verified download, atomic replace and rollback; update lines in `status`/`top`; opt-in `update_available` alert.
- **Bootstrap sync** (v0.6.0): `nomctl bootstrap` downloads a verified chain snapshot and swaps it in, keeping the previous data unless `--discard`.
- **Config editor** (v0.12.0): `nomctl config show|get|set|unset|edit` against go-zenon's schema, backups, `--restart`.
- **Pillar producer** (v0.10.0): `nomctl pillar setup` creates the producer key with go-zenon's wallet code and wires it into `config.json`, as znn-controller's Deploy did.
- **Non-root alerts daemon** (v0.9.0): the daemon runs as a locked `nomctl` user with no capabilities; a root oneshot probe feeds `fds_high`; older nodes migrate on upgrade.
- **Orchestrator** (v0.7.0): submenu and `nomctl orchestrator` with hard reset, status and logs; more orchestrator functions land here.

## Next

- **Wallet and config backup**: a separate small archive of `wallet/` and `config.json`, optionally encrypted, never pruned by the chain-data retention rule.
- **Network check**: confirm port 35995/TCP is reachable from outside and count inbound versus outbound peers.
- **Metrics exporter**: a Prometheus `/metrics` endpoint fed by the sampler, scraped by the analytics stack, plus a nomctl Grafana dashboard.

## Later

- **Benchmark**: disk, memory and CPU against node requirements before deploying.
- **Doctor**: run the sampler and crash-marker grep and print the matching diagnosis from the signatures table.
- **`nomctl pillars`**: list all pillars with weight and momentum stats.
- A generic webhook output for alerts, alongside Telegram.

## Not planned

Modes (liteserver, validator, pools), a remote controller for many nodes, fleet telemetry, and account or governance operations that belong in `znn-cli`. The Go SDK cannot be used until it builds without cgo.
