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
| `NOMCTL_REPO_URL` | `https://github.com/zenon-network/go-zenon.git` | Git repository to build |
| `NOMCTL_BRANCH_NAME` | `master` | Git branch to build |
| `NOMCTL_BINARY_NAME` | `znnd` | Node binary name (also the `cmd/` package built) |
| `NOMCTL_SERVICE_NAME` | `go-zenon` | systemd service name |
| `NOMCTL_GO_VERSION` | `1.23.0` | Go toolchain version used to build the node |
| `NOMCTL_PILLAR_NAME` | unset | Pillar shown by `status`/`top` when alerts are not set up (the alerts config takes precedence) |
| `NOMCTL_BACKUP_DIR` | `/backup` | Directory that stores backup archives |
| `NOMCTL_MAX_BACKUPS` | `7` | Number of backups to retain |
| `NOMCTL_BACKUP_CADENCE_DAYS` | `0` | Days between scheduled backups (0 = every run) |
| `NOMCTL_BACKUP_HOUR` | unset | Hour (0-23) for scheduled backups; unset = derived, between 02:00 and 04:59 |
| `NOMCTL_MIN_FREE_SPACE_KB` | `15728640` | Minimum free space in the backup directory (15 GB) |
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
| `/etc/systemd/system/nomctl-backup.service`, `.timer` | `backup --schedule` |
| `/etc/systemd/system/nomctl-alerts.service` | `alerts setup` |
| `/etc/nomctl/alerts.json` (0600) | `alerts setup`, `alerts set`, `enable`, `disable` |
| `/run/nomctl/alerts-state.json` | the alerts daemon, read by `alerts status` |
| `/run/nomctl.lock` | backup, restore, resync, deploy while running |
| `/etc/systemd/timesyncd.conf` | pre-flight, only if NTP is not `time.cloudflare.com` |

## The alerts file

`/etc/nomctl/alerts.json` holds the relay URL, node id and secret, the node name, the pillar name, the sampling interval, and per-alert `enabled` and `thresholds`. Edit it with `nomctl alerts set`, `enable` and `disable`; hand edits are picked up on the next `systemctl kill -s HUP nomctl-alerts`, with out-of-range thresholds clamped. The service unit carries the service, data and backup settings that were in effect at setup, so the daemon watches the node you configured even if the environment changes.
