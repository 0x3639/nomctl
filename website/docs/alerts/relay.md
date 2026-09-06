---
title: Running the relay
description: "Run your own nomctl-relay on Coolify: bot creation, environment variables, volume, domain, trusted proxies."
---

The relay is one container with one SQLite file. It holds the Telegram bot token; nodes never see it. The community relay at `https://alerts.zenon.info` is what released binaries use by default; this page is for running your own.

## 1. Create the bot

Talk to `@BotFather` in Telegram, send `/newbot`, choose a name and a username, and save the token. Optionally `/setcommands` with:

```
start - get a pairing code for a new node
nodes - list your paired nodes
mute - silence an alert: /mute NAME ALERT [DURATION]
unmute - resume an alert: /unmute NAME ALERT
unpair - forget a node: /unpair NAME
help - show help
```

## 2. Deploy on Coolify

Add a **Docker Image** resource with `ghcr.io/0x3639/nomctl-relay:latest` (or a Docker Compose resource using `deploy/relay/docker-compose.yml` from the repository). Then:

- **Environment**: `RELAY_TELEGRAM_TOKEN` (mark it as a secret), `RELAY_PUBLIC_URL=https://alerts.example.org`, and `RELAY_TRUSTED_PROXIES` set to the network Coolify's proxy connects from. The Docker bridge ranges `10.0.0.0/8,172.16.0.0/12,192.168.0.0/16` work when only the proxy can reach the container; if other hosts on those ranges can reach port 8080 directly, narrow it to the proxy's own subnet, since anything in the list may claim any client address.
- **Storage**: a persistent volume at `/data`. The image ships `/data` owned by the container user, so a fresh named volume works as is.
- **Domain**: `https://alerts.example.org:8080`, the scheme so Coolify requests a certificate, the port so the proxy reaches the container.
- **Health check**: `GET /healthz` on port 8080.

Redeploy. The container logs should show one `relay listening` line. Open `/healthz` in a browser (`ok`) and send `/start` to the bot.

| Variable | Default | Meaning |
|---|---|---|
| `RELAY_TELEGRAM_TOKEN` | required | bot token |
| `RELAY_DB` | `/data/relay.db` | SQLite path |
| `RELAY_LISTEN` | `:8080` | listen address |
| `RELAY_SILENT_AFTER` | `5m` | heartbeat gap before `node_silent` |
| `RELAY_PUBLIC_URL` | empty | shown in the `/start` reply |
| `RELAY_TRUSTED_PROXIES` | empty | CIDRs of reverse proxies whose `X-Forwarded-For` is trusted for per-client rate limiting; unset means the header is ignored |
| `RELAY_LOG_LEVEL` | `info` | `info` or `debug` |

## 3. Point nodes at it

`sudo nomctl alerts setup --relay https://alerts.example.org`, or `NOMCTL_RELAY_URL`. To bake the URL into your own builds, set the `NOMCTL_RELAY_URL` repository variable (releases) or `make build RELAY_URL=...`.

## How it behaves

- Pairing codes are single use, expire after 10 minutes, and are rate limited per client address.
- Every node request is signed (HMAC-SHA256 with the node's secret, 5-minute replay window). Store or transport errors never turn into "unpaired"; only an unknown node id does.
- An alert is recorded as delivered only after Telegram accepted it; failed sends return an error to the node, which retries.
- Upgrades: pull the new image and restart; the schema migrates itself and pairings live in the volume. Back up `/data/relay.db`.
