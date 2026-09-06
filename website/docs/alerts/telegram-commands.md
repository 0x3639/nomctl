---
title: Telegram commands
description: "Bot commands: /start, /nodes, /mute, /unmute, /unpair."
---

Send these to the bot in the chat your nodes are paired to. Names are matched within your chat only.

| Command | Effect |
|---|---|
| `/start` | get a pairing code for a new node |
| `/nodes` | list your nodes with host, last heartbeat age and the last summary (sync state, height, peers, restarts); silent nodes are marked |
| `/mute NAME ALERT [DURATION]` | silence one alert or `all` for a node; durations like `30m`, `2h`, `1d`, default 24h |
| `/unmute NAME ALERT` | resume one alert or `all` |
| `/unpair NAME` | forget a node; its daemon stops reporting and `nomctl alerts setup` pairs it again |
| `/help` | this list |

Muting is applied on the relay: the node still reports, the state is recorded, and delivery resumes with the next transition after the mute expires or is lifted.

If you block the bot and later unblock it, sending any command restores delivery for your nodes.
