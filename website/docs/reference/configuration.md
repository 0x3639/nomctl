---
title: Configuration
description: "Every NOMCTL_* environment variable with its default, and the files nomctl writes."
---

Settings come from `NOMCTL_*` environment variables; command-line flags override them. `nomctl env` prints this table from the binary.

| Variable | Default | Description |
|---|---|---|
| `NOMCTL_DEBUG` | `false` | Verbose logging; stream external command output to the terminal |
| `NOMCTL_LOG_FILE` | `/var/log/nomctl.log` | Plain-text log file (also receives external command output) |
| `NOMCTL_SKIP_PREFLIGHT` | `false` | Skip the CPU/RAM/NTP/Internet pre-flight checks |
| `NOMCTL_WORK_DIR` | `/opt/nomctl` | Where the Go toolchain and source checkout are kept |
| `NOMCTL_INSTALL_DIR` | `/usr/local/bin` | Where the node binary is installed |
| `NOMCTL_ZNN_DIR` | `/root/.znn` | Node data directory used by backup, restore and resync. Not passed to `znnd`, which uses its own default |
| `NOMCTL_REPO_URL` | `https://github.com/zenon-network/go-zenon.git` | Git repository for `deploy` and `upgrade-node` |
| `NOMCTL_BRANCH_NAME` | `master` | Git branch for `deploy` and `upgrade-node` |
| `NOMCTL_BINARY_NAME` | `znnd` | Node binary name (also the `cmd/` package built) |
| `NOMCTL_SERVICE_NAME` | `go-zenon` | systemd service name |
| `NOMCTL_GO_VERSION` | `1.23.0` | Go toolchain version used to build the node |
| `NOMCTL_PILLAR_NAME` | unset | Pillar shown by `status`/`top` when alerts are not set up (the alerts config takes precedence) |
| `NOMCTL_REPO` | `0x3639/nomctl` | GitHub repository `nomctl upgrade` downloads releases from |
| `NOMCTL_GITHUB_TOKEN` | unset | Optional GitHub token with Contents read access to `zenon-network/go-zenon-releases`; required while private, redacted from logs |
| `NOMCTL_UPDATE_CHECK` | `true` | Check GitHub for newer nomctl and go-zenon in `status`/`top` and the `update_available` alert (cached 1 h) |
| `NOMCTL_BOOTSTRAP_URL` | the snapshot published for this release | Snapshot `.zip` for [`nomctl bootstrap`](/guide/bootstrap); a `.hash` sidecar must sit next to it |
| `NOMCTL_RPC_TIMEOUT` | `3s` | Per-call timeout for the node's JSON-RPC; raise it on slow disks |
| `NOMCTL_ORCHESTRATOR_SERVICE` | `orchestrator` | systemd unit for [`nomctl orchestrator`](/guide/orchestrator) |
| `NOMCTL_ORCHESTRATOR_DIR` | `/root/.orchestrator` | Orchestrator state directory; `hard-reset` deletes `queues/` and `events/` under it |
| `NOMCTL_BACKUP_DIR` | `/backup` | Directory that stores backup archives |
| `NOMCTL_MAX_BACKUPS` | `7` | Maximum number of manual backups to retain (1–30); daily cron jobs use 30 |
| `NOMCTL_BACKUP_RETENTION_DAYS` | `7` | Maximum age of local chain backups, in days (1–30) |
| `NOMCTL_BACKUP_CADENCE_DAYS` | `0` | Minimum age of newest backup before a manual run (0 = every run); daily cron jobs always run |
| `NOMCTL_BACKUP_HOUR` | unset | Hour (0-23) for scheduled backups; unset = derived, between 02:00 and 04:59 |
| `NOMCTL_MIN_FREE_SPACE_KB` | `15728640` | Minimum free space left after backup writes (15 GiB); the 75% disk limit also applies |
| `NOMCTL_NODE_EXPORTER_VERSION` | `1.6.1` | Prometheus node_exporter version |
| `NOMCTL_PROMETHEUS_VERSION` | `2.47.0` | Prometheus version |
| `NOMCTL_INFINITY_PLUGIN_VERSION` | `2.10.0` | Grafana Infinity datasource plugin version |
| `NOMCTL_GRAFANA_ADMIN_USER` | `admin` | Grafana admin user |
| `NOMCTL_GRAFANA_ADMIN_PASSWORD` | `admin` | Grafana admin password; a non-default value is applied to Grafana on install |
| `NOMCTL_GRAFANA_HTTP_ADDR` | `127.0.0.1` | Address Grafana listens on (`0.0.0.0` to expose it) |
| `NOMCTL_RELAY_URL` | built in | Alerts relay used by `alerts setup` when `--relay` is not given |

## Files nomctl writes

| Path | Written by |
|---|---|
| `/etc/systemd/system/go-zenon.service` | `deploy` |
| `/etc/cron.d/nomctl-backup` | `backup schedule` (or `backup --schedule` after a backup) |
| `/etc/systemd/system/nomctl-alerts.service` | `alerts setup` |
| `/etc/nomctl/alerts.json` (0600) | `alerts setup`, `alerts set`, `enable`, `disable` |
| `/run/nomctl/alerts-state.json` | the alerts daemon, read by `alerts status` |
| `/run/nomctl.lock` | service controls, backup, restore, resync, deploy, upgrade-node while running |
| `/usr/local/bin/znnd.previous` | `upgrade-node`, previous node binary (path follows install directory and binary name settings) |
| `/backup/snapshot-*` | Uncompressed chain-data snapshot retained when backup finalization fails |
| `/run/nomctl-system/update-check.json` | the cached update check for root commands |
| `/run/nomctl-system/process-probe.json` | root-written metrics, readable by the alerts daemon |
| `/run/nomctl/update-check.json` | the alerts daemon's cached update check |
| `/usr/local/bin/nomctl.previous` | `upgrade`, for `--rollback` |
| `/etc/systemd/timesyncd.conf` | pre-flight, only if NTP is not `time.cloudflare.com` |

## The alerts file

`/etc/nomctl/alerts.json` holds the relay URL, node id and secret, the node name, the pillar name, the sampling interval, and per-alert `enabled` and `thresholds`. Edit it with `nomctl alerts set`, `enable` and `disable`; hand edits are picked up on the next `systemctl kill -s HUP nomctl-alerts`, with out-of-range thresholds clamped. The service unit carries the service, data and backup settings that were in effect at setup, so the daemon watches the node you configured even if the environment changes.
