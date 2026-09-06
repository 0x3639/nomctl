---
title: Pairing a node
---

## 1. Get a code

Open the nomctl alerts bot in Telegram and send `/start`. It replies with an 8-character pairing code, valid for 10 minutes and usable once. You can hold up to five unused codes.

## 2. Pair

On the node:

```bash
sudo nomctl alerts setup
```

It asks for the code and a **name** for the node (default: the hostname), pairs with the relay, writes `/etc/nomctl/alerts.json`, installs and starts `nomctl-alerts.service`, and sends a test message. Your chat receives "paired and reporting" and then the test.

Flags skip the prompts, which is how to script it:

```bash
sudo nomctl alerts setup --code AB3K7QWX --name pillar-1
```

Released binaries default to the community relay at `https://alerts.zenon.info`. `--relay` or `NOMCTL_RELAY_URL` point at another one.

## The name is also the pillar name

If a registered pillar has the same name as the node, setup says "monitoring pillar NAME (rank N)", stores it, and the `pillar_missed` alert becomes active. `nomctl status` and `top` then show the pillar's rank and produced versus expected momentums for the epoch.

If the names differ, set it afterwards; the value is checked against the pillar list:

```bash
sudo nomctl alerts set pillar.name MyPillar
sudo nomctl alerts set pillar.name ""      # clear
```

## More than one node

Pair as many nodes as you like to the same chat. Names must be unique per chat, and every message starts with the node's name.

## Managing on the node

```bash
sudo nomctl alerts status                 # pairing, service, current state of each alert
sudo nomctl alerts list                   # alerts and thresholds
sudo nomctl alerts set disk_low.min_free_gb 30
sudo nomctl alerts disable backup_stale
sudo nomctl alerts enable backup_stale
sudo nomctl alerts test
sudo nomctl alerts unpair                 # keeps credentials if the relay is unreachable; --force removes them anyway
```

Threshold and switch changes reach the running daemon immediately. Duration thresholds are limited to 30 minutes, the amount of history the daemon keeps.

## If something fails during setup

If pairing succeeds but the service cannot be installed, setup undoes the pairing at the relay so you do not get a `node silent` alert for a node that never reported. If even that fails, the credentials are kept and setup tells you to run `nomctl alerts unpair`.
