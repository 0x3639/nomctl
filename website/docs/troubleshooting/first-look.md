---
title: First look
description: "How to read nomctl status and nomctl top: service, sync, frontier, pillar, process and host lines."
---

```bash
sudo nomctl doctor
sudo nomctl status
```

Start with [Check my node](./doctor.md) for a diagnostic report and suggested
actions, including znnd's version, commit and installed/running executable
hashes. `status` gives the compact measurement summary shown below.

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
- **`node rpc check failed`** (`node rpc unreachable` in older versions): read the error after the endpoint. A refused connection means nothing accepted the connection; an HTTP error means a listener answered but rejected the request. The process and host measurements remain valid.
- **Process**: open files near the 32768 limit, or memory close to the host total, predict the two most common crashes.
- **Host**: less than 15 GB free on the data directory stops backups and will eventually stop the node. IO pressure above about 50% on a syncing node means the disk is the bottleneck.

## Live

```bash
sudo nomctl top
```

The same values refreshed every two seconds (`--interval`), with a sync progress bar and a sparkline of momentums per second. `q` quits. Both commands are read-only.

`status` and `top` read `RPC.EnableHTTP`, `RPC.HTTPHost`, and `RPC.HTTPPort`
from `<NOMCTL_ZNN_DIR>/config.json` (normally `/root/.znn/config.json`). Missing
settings use the node defaults. Wildcard binds (`0.0.0.0` or `::`) are contacted
through loopback. Disabled HTTP or invalid/unreadable settings produce an
explicit error instead of probing an assumed port. Restart the node after
changing its listener settings; the dashboard reads the file again each sample.

For a public node behind Caddy, nomctl uses the **internal HTTP listener**.
For example, `HTTPHost: "127.0.0.1"` and `HTTPPort: 3597` select
`http://127.0.0.1:3597`, even when Caddy exposes HTTPS on `35997` and WSS on
`35998`. No public URL or TLS setting is needed in nomctl. An older version
hard-coded `35997`; if that port belongs to Caddy, its response can be
`HTTP 400: Client sent an HTTP request to an HTTPS server.`

The unprivileged alerts daemon receives only the resolved connection settings
through its root-owned probe, refreshed every 30 seconds. It does not receive
the node configuration or producer secrets. A missing, stale, or mismatched
probe reports unavailable RPC settings instead of guessing a port.
