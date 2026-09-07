---
title: Alerts overview
description: "Telegram alerts for Zenon nodes through a shared relay: how it works, what you get, and privacy."
---

nomctl can message you on Telegram when the node needs attention, and again when it recovers.

All operators share one bot; each operator only sees alerts for the nodes they paired. The bot token lives on a small relay service, never on your node. Your node signs every report with a secret created at pairing, and the relay forwards it to your chat only.

```
node (nomctl-alerts.service)  ──HTTPS, signed──▶  relay (alerts.zenon.info)  ──Bot API──▶  Telegram
```

## What you get

- A message when something goes wrong and a matching message when it recovers.
- A reminder every 10 minutes while a problem persists, unless you mute it.
- A `node silent` alert from the relay when the whole machine stops reporting, which the node itself could never send.
- Per-alert thresholds and switches on the node, mutes from Telegram.

## The three steps

1. Send `/start` to the bot and get a pairing code.
2. Run `sudo nomctl alerts setup` on the node.
3. Read the test message in your chat.

Details in [Pairing](/alerts/pairing), the full list of alerts in [Rules](/alerts/rules), the chat commands in [Telegram commands](/alerts/telegram-commands), and how to run your own relay in [Relay](/alerts/relay).

:::warning[Privacy notice: your node's IP address is visible to the relay]

Once paired, your node sends a heartbeat to **alerts.zenon.info** every 30 seconds and forwards alerts through it. The relay operator can see your node's **public IP address**, host name, node name and status summary. The IP address is used only to rate-limit pairing and is not stored in the relay database, but it is visible to the relay and may appear in the logs of the proxy in front of it.

If your pillar's IP address must stay private, [run your own relay](/alerts/relay) and point nodes at it with `NOMCTL_RELAY_URL`, or do not pair.

:::

## Privacy

The relay stores your chat id, the node name and host name, the last heartbeat summary (sync state, height, peers, restarts), mutes, and when each alert was last sent. Alert text is forwarded, not stored. The relay never sends anything to a node. Unpairing from either side invalidates the node's secret immediately.
