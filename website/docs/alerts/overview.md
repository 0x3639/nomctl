---
title: Alerts overview
description: "Telegram alerts for Zenon nodes through a shared relay: architecture, setup, privacy, and system access."
---

nomctl can message you on Telegram when the node needs attention, and again when it recovers.

Alerting has two components:

- **The node-side alerts daemon** (`nomctl-alerts.service` plus the `nomctl-alerts-probe.timer` helper) — runs on your node, samples health every 30 seconds, evaluates alert rules, and pushes signed reports outward.
- **The relay** (`nomctl-relay`) — a container that holds the Telegram bot token (never stored on your node), forwards each node's reports only to its paired chat, and raises `node_silent` if heartbeats stop.

All operators using the community relay share one bot; each operator only sees alerts for the nodes they paired. Your node signs every report with an HMAC-SHA256 secret created at pairing.

```
node (nomctl-alerts.service)  ──HTTPS, signed──▶  relay (alerts.zenon.info)  ──Bot API──▶  Telegram
```

## What you get

- A message when something goes wrong and a matching message when it recovers.
- A reminder every 10 minutes while a problem persists, unless you mute it.
- A `node silent` alert from the relay when the whole machine stops reporting, which the node itself could never send.
- Per-alert thresholds and switches on the node, mutes from Telegram.

## Deploying in three steps

1. Send `/start` to the bot and get a pairing code.
2. Run `sudo nomctl alerts setup` on the node.
3. Confirm the "paired and reporting" and test messages in your chat.

Details and step-by-step setup/upgrade instructions are in [Pairing](/alerts/pairing), the full list of alerts in [Rules](/alerts/rules), the chat commands in [Telegram commands](/alerts/telegram-commands), and self-hosting instructions in [Relay](/alerts/relay).

:::warning[Privacy notice: your node's IP address is visible to the relay]

Once paired, your node sends a heartbeat to **alerts.zenon.info** every 30 seconds and forwards alerts through it. The relay operator can see your node's **public IP address**, host name, node name and status summary. The IP address is used only to rate-limit pairing and is not stored in the relay database, but it is visible to the relay and may appear in the logs of the proxy in front of it.

If your pillar's IP address must stay private, [run your own relay](/alerts/relay) and point nodes at it with `--relay` (or `sudo NOMCTL_RELAY_URL=...`), or do not pair.

:::

## System access and privacy summary

| Component | Runs as | Network access | Access boundaries & sensitive files |
|---|---|---|---|
| `nomctl-alerts.service` | locked user `nomctl` (no capabilities) | local HTTP (`znnd` RPC) and outbound HTTPS to relay (every 30 s) and GitHub update check (hourly, if enabled); no listening ports | cannot access `/root` or `/home` (`ProtectHome=true`), root-only files such as `0600` `config.json`/wallet keys, `znnd` process memory, or the Telegram bot token |
| `nomctl-alerts-probe.service` | `root` (oneshot every 30 s) | no direct host IP network (`PrivateNetwork=true`; local filesystem Unix sockets remain accessible) | `ProtectHome=read-only` leaves `/root` visible and read-only to this root process (cannot write there); the probe only reads `config.json`'s RPC section and disk stats and does not read `/root/.znn/wallet/`, producer keys/passwords, or chain databases |
| `nomctl-relay` | distroless non-root (`uid 65532`, no shell) | inbound HTTPS via proxy, outbound to `api.telegram.org` (or test-only `RELAY_TELEGRAM_URL` if set) | no access back into any node (nodes only push) |

For the complete file, RPC, `/proc`, and storage disclosure, see [What runs on the node](/alerts/pairing#what-runs-on-the-node) and [What the relay accesses and stores](/alerts/relay#what-the-relay-accesses-and-stores). Unpairing from either side invalidates the node's secret immediately.
