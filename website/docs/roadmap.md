---
title: Roadmap
---

Planned features in priority order. Each gets a design spec in the repository under `docs/superpowers/specs/` before code. Several are inspired by [MyTonCtrl](https://github.com/ton-blockchain/mytonctrl), the TON node controller.

## Done

- **Alerting** (v0.3.0): Telegram through a shared relay, pairing by code, per-alert thresholds, mutes, `node_silent`.
- **Pillar awareness** (v0.4.0): the node name doubles as the pillar name; `status` and `top` show production; `pillar_missed` and `momentums_stalled` alerts.

## Next

- **Self-update**: `nomctl upgrade` downloads the latest release, verifies the checksum and replaces the binary; `status` shows when nomctl or the node has an update available.
- **Wallet and config backup**: a separate small archive of `wallet/` and `config.json`, optionally encrypted, never pruned by the chain-data retention rule.
- **Config editor**: `nomctl config show|get|set` for `config.json` with validation and a backup of the previous file.
- **Network check**: confirm port 35995/TCP is reachable from outside and count inbound versus outbound peers.
- **Metrics exporter**: a Prometheus `/metrics` endpoint fed by the sampler, scraped by the analytics stack, plus a nomctl Grafana dashboard.

## Hardening follow-ups

- The generated `go-zenon` unit stops the node with `pkill -9 znnd`, inherited from the bash toolkit. On a host running more than one znnd it would kill all of them; the unit should target `$MAINPID` or rely on systemd's own kill handling.
- `analytics install` does not change Grafana's initial `admin` / `admin` password; see [Analytics](/guide/analytics) for the manual steps. A future release should set it from `NOMCTL_GRAFANA_ADMIN_PASSWORD` on first install and bind Grafana to localhost.

## Later

- **Benchmark**: disk, memory and CPU against node requirements before deploying.
- **Doctor**: run the sampler and crash-marker grep and print the matching diagnosis from the signatures table.
- **Bootstrap sync**: download a trusted chain snapshot with a published hash so a fresh node syncs in minutes.
- **`nomctl pillars`**: list all pillars with weight and momentum stats.
- A generic webhook output for alerts, alongside Telegram.

## Not planned

Modes (liteserver, validator, pools), a remote controller for many nodes, fleet telemetry, and account or governance operations that belong in `znn-cli`. The Go SDK cannot be used until it builds without cgo.
