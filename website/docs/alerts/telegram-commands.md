---
title: Telegram commands
description: "Bot commands: /start, /nodes, /mute, /unmute, /unpair."
---

Send these to the bot in the chat your nodes are paired to. Names are matched within your chat only.

| Command | Effect |
|---|---|
| `/start` | get a pairing code for a new node |
| `/nodes` | one card per node, see below; silent nodes are marked 🔴 |
| `/mute NAME ALERT [DURATION]` | silence one alert or `all` for a node; durations like `30m`, `2h`, `1d`, default 24h |
| `/unmute NAME ALERT` | resume one alert or `all` |
| `/unpair NAME` | forget a node; its daemon stops reporting and `nomctl alerts setup` pairs it again |
| `/help` | this list |

## The /nodes card

Each node shows what its last heartbeat carried, one labelled line per topic:

```
🟢 pillar-1 · go-zenon-hot-fix · seen 15s ago
Sync syncing 11,107,882 / 14,135,103 (78.6%) · 6.2 mom/s · ETA 5d 14h
Frontier 11,108,343 · 3s ago
Peers 28 · restarts 0 · up 2d 4h
Pillar MyPillar rank 11 · 118 / 121 produced this epoch
Node znnd v0.0.7 (a1b2c3d) · nomctl 0.7.1
Host load 1.2 · mem 3.1 GB / 7.8 GB free · disk 190.0 GB / 500.0 GB free (38%)
Process cpu 45% · rss 2.1 GB
Update nomctl 0.7.2 available
```

- **Sync**: state, height against the target, momentum rate and ETA while syncing.
- **Frontier**: the newest momentum and its age, or `ledger busy` while the node is inserting momentums flat out (see [First look](/troubleshooting/first-look)).
- **Peers**: connected peers, service restarts, time since the service started.
- **Pillar**: only when a pillar name is configured; rank and this epoch's production, or why the pillar was not found.
- **Host** and **Process**: load, free memory, free disk on the data directory, znnd CPU and resident memory.
- **Update**: only when a newer nomctl release exists or the go-zenon branch has new commits.

Lines a node did not report are left out, so nodes on releases before 0.8.0 show the short form: sync state, height, peers and restarts.

Muting is applied on the relay: the node still reports, the state is recorded, and delivery resumes with the next transition after the mute expires or is lifted.

If you block the bot and later unblock it, sending any command restores delivery for your nodes.
