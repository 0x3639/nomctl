---
title: Alert rules
description: "Every alert rule, its threshold, which failure it covers, and how retries and reminders behave."
---

The daemon samples every 30 seconds and keeps 30 minutes of history. Each rule is evaluated over that history, so "for 5 minutes" means five minutes of consecutive evidence, not one bad sample.

| Alert | Severity | Fires when | Default thresholds |
|---|---|---|---|
| `service_down` | critical | the service is not active for two samples in a row | |
| `crash_loop` | critical | systemd restarted the node at least twice within the window | `window_minutes=10 count=2` |
| `sync_stalled` | critical | the node reports synced but its newest momentum is older than 2 minutes, twice in a row | |
| `momentums_stalled` | critical | the frontier height has not moved for the window while the service runs and RPC answers, whatever sync state the node claims | `minutes=5` |
| `sync_behind` | warning | syncing, and the gap to the target height has not shrunk over the window | `minutes=10` |
| `not_enough_peers` | warning | fewer than `min_peers` peers, or the node itself reports not-enough-peers, for the window | `min_peers=3 minutes=5` |
| `pillar_missed` | critical | over the window your pillar's expected momentums grew by `missed` more than its produced count; an epoch rollover restarts the window | `minutes=30 missed=2` |
| `disk_low` | warning | the data directory's filesystem has less than `min_free_gb` free | `min_free_gb=15` |
| `memory_high` | warning | znnd's resident memory exceeds `pct` of host memory | `pct=85` |
| `fds_high` | warning | open files exceed `pct` of the limit (32768) | `pct=80` |
| `backup_stale` | warning | the backup timer is enabled and the newest archive is older than cadence + 1 day | |
| `rpc_unreachable` | warning | the service is active but the local RPC has not answered for the window | `minutes=5` |
| `node_silent` | critical | raised by the relay: no heartbeat from the node for 5 minutes | relay setting |
| `update_available` | info, **off by default** | a newer nomctl release exists, or the configured go-zenon branch has commits beyond the running build; sent once, no reminders | |

## Which alert for which failure

| Situation | You get |
|---|---|
| znnd halts or is killed | `service_down` in about a minute, then `service back up` |
| the whole machine loses power or network | `node_silent` after 5 minutes, then `node reporting again` |
| znnd keeps crashing and systemd keeps restarting it | `crash_loop` |
| go-zenon runs but momentums stop advancing | `momentums_stalled` within 5 minutes, regardless of what the node says about its sync state; `sync_stalled` sooner if it claims to be synced |
| initial sync makes no progress | `sync_behind`, often with `not_enough_peers` explaining why |
| your pillar is online but skipping its production slots | `pillar_missed` |
| the disk is filling up | `disk_low`; backups also stop below the same 15 GB minimum |
| about to be killed by the OOM killer, or hitting the file limit | `memory_high`, `fds_high` |

CPU is deliberately not an alert: a syncing node runs hot for hours, and the failures that matter have their own alerts. Load is visible in `nomctl status` and `top`.

## Behaviour

- Every alert has a recovery message.
- A firing alert repeats every 10 minutes until it clears.
- The first evaluation after the daemon starts only establishes a baseline and announces "alerts started"; nothing else is sent until a condition is confirmed on a later sample.
- A report the relay could not deliver (Telegram outage) is answered with an error, and one the relay never received (relay down) times out; in both cases the node keeps the transition pending and re-sends it on its next sample until the relay confirms delivery. The relay records an alert as sent only after Telegram accepted it. The one gap is the relay's own `node_silent` while Telegram is down: it is retried every 30 seconds by the relay until it goes through.
