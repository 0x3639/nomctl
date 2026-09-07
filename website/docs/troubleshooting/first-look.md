---
title: First look
description: "How to read nomctl status and nomctl top: service, sync, frontier, pillar, process and host lines."
---

```bash
sudo nomctl status
```

```
Service   go-zenon active (running), pid 1234, 0 restarts, up 3d 4h
Node      znnd v0.0.7 (a1b2c3d), syncing 1,234,567 / 2,000,000 (61.7%), 5.2 mom/s, ETA 1d 16h
Peers     14 connected
Frontier  height 1,234,567, 3s ago
Pillar    MyPillar rank 12, produced 118 / 121 expected this epoch, 3 missed
Process   cpu 42.0%, rss 1.9 GiB, threads 38, open files 412 / 32768
Host      load 1.20 0.90 0.80, mem 3.1 GiB / 7.8 GiB available, /root/.znn 210.0 GiB free
Pressure  cpu 2.1%, io 15.4%, mem 0.0%
```

CPU % and the sync rate are measured over `--wait` (default 2 seconds). `--json` prints the raw sample for scripts.

- **Service**: `active (running)` with a restart count that is not climbing is healthy. A growing count is a crash loop; collect a [support bundle](/troubleshooting/support-bundle) with `--watch`.
- **Node**: `syncing` with a rate above zero means progress. `synced` with a frontier older than two minutes is shown as `[STALLED]`; restart the service. `not enough peers` usually means port 35995/TCP is blocked inbound or the host has no outbound connectivity.
- **Frontier**: the newest momentum the node has. Momentums arrive roughly every 10 seconds, so on a healthy synced node this is always a few seconds old.
- **Pillar** (only when a pillar name is configured): produced should track expected through the epoch; a growing gap means missed production slots.
- **`Frontier   ledger busy`**: the stats calls answered but `ledger.getFrontierMomentum` did not within `NOMCTL_RPC_TIMEOUT` (3 s). That call waits on the lock momentum insertion holds, so it happens on a node inserting momentums flat out, typically while catching up after a deploy or bootstrap. The sync line still shows height and rate; as long as the height climbs, nothing is wrong. Raise the timeout on a slow disk. A ledger that stays busy while the height stops moving is a hung insert, and `momentums_stalled` reports it.
- **`node rpc unreachable`**: the process is not up, or its HTTP RPC on port 35997 was disabled in `config.json`. The rest of the output is still valid.
- **Process**: open files near the 32768 limit, or memory close to the host total, predict the two most common crashes.
- **Host**: less than 15 GB free on the data directory stops backups and will eventually stop the node. IO pressure above about 50% on a syncing node means the disk is the bottleneck.

## Live

```bash
sudo nomctl top
```

The same values refreshed every two seconds (`--interval`), with a sync progress bar and a sparkline of momentums per second. `q` quits. Both commands are read-only and need no configuration.
