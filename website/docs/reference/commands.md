---
title: Commands
---

Every menu action is a subcommand. All require root except `--help`, `--version`, `env` and `completion`.

## Global flags

| Flag | Effect |
|---|---|
| `--debug` | verbose logging; external command output shown on the terminal instead of hidden behind spinners |
| `--log-file PATH` | log file (default `/var/log/nomctl.log`) |
| `--skip-preflight` | skip the CPU/RAM/NTP/Internet checks |

## Node lifecycle

| Command | Notes |
|---|---|
| `nomctl deploy [--repo URL] [--branch NAME]` | [Deploy](/guide/deploy) |
| `nomctl start` / `stop` / `restart` | [Service control](/guide/service) |
| `nomctl logs [-f] [-n LINES]` | last 20 lines, or follow |
| `nomctl resync` | wipe chain data, keep wallet and config |
| `nomctl backup [--max-backups N] [--cadence DAYS] [--hour HOUR] [--schedule]` | [Backups](/guide/backups) |
| `nomctl restore [--file FILE]` | picker when `--file` is omitted |
| `nomctl analytics install` | [Analytics stack](/guide/analytics) |

## Observability

| Command | Notes |
|---|---|
| `nomctl status [--json] [--wait 2s]` | one-screen summary |
| `nomctl top [--interval 2s]` | live dashboard, `q` to quit |
| `nomctl support-bundle [--watch] [--since "12 hours ago"] [--output DIR] [--poll 10s] [--timeout 0]` | [Support bundle](/troubleshooting/support-bundle) |

## Alerts

| Command | Notes |
|---|---|
| `nomctl alerts setup [--code CODE] [--name NAME] [--relay URL]` | pair and start the service |
| `nomctl alerts status` | pairing, service, per-alert state |
| `nomctl alerts list` | alerts with thresholds |
| `nomctl alerts enable ALERT` / `disable ALERT` | |
| `nomctl alerts set ALERT.KEY VALUE` | thresholds, e.g. `disk_low.min_free_gb 20`; also `pillar.name NAME` |
| `nomctl alerts test` | send a test message |
| `nomctl alerts unpair [--force]` | |
| `nomctl alerts run` | the daemon; used by `nomctl-alerts.service` |

## Other

| Command | Notes |
|---|---|
| `nomctl` | the [menu](/guide/menu) |
| `nomctl env` | every `NOMCTL_*` variable with its default |
| `nomctl completion bash\|zsh\|fish` | shell completion |
| `nomctl --version` | version, commit, build date, platform |
