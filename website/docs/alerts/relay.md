---
title: Running the relay
description: "Deploy and upgrade your own nomctl-relay on Coolify or Docker, plus what the relay accesses and stores."
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

## 2. Deploy the container

No inbound webhook from Telegram is needed: the relay long-polls `api.telegram.org`. Put an HTTPS reverse proxy in front of container port `8080` so node heartbeats and alerts travel over TLS.

### Option A: Coolify

Add a **Docker Image** resource with `ghcr.io/0x3639/nomctl-relay:latest` (or a Docker Compose resource using `deploy/relay/docker-compose.yml` from the repository). Then:

- **Environment**: `RELAY_TELEGRAM_TOKEN` (mark it as a secret), `RELAY_PUBLIC_URL=https://alerts.example.org`, and `RELAY_TRUSTED_PROXIES` set to the network Coolify's proxy connects from. The Docker bridge ranges `10.0.0.0/8,172.16.0.0/12,192.168.0.0/16` work when only the proxy can reach the container; if other hosts on those ranges can reach port 8080 directly, narrow it to the proxy's own subnet, since anything in the list may claim any client address.
- **Storage**: a persistent volume at `/data`. The image ships `/data` owned by `uid 65532`, so a fresh named volume works as is.
- **Domain**: `https://alerts.example.org:8080`, the scheme so Coolify requests a certificate, the port so the proxy reaches the container.
- **Health check**: `GET /healthz` on port 8080.

Redeploy and check the logs for `relay listening`.

### Option B: Standalone Docker or Docker Compose

With a named volume (inherits `65532:65532` ownership automatically) behind a local HTTPS reverse proxy (Caddy, Nginx, Traefik):

```bash
docker run -d --name nomctl-relay --restart unless-stopped \
  -p 127.0.0.1:8080:8080 \
  -e RELAY_TELEGRAM_TOKEN='123456:your-bot-token' \
  -e RELAY_PUBLIC_URL='https://alerts.example.org' \
  -e RELAY_TRUSTED_PROXIES='127.0.0.1/32' \
  -v relay-data:/data \
  ghcr.io/0x3639/nomctl-relay:latest
```

If you bind-mount a host directory instead of a named volume, set its ownership first because the container runs as non-root `uid 65532`:

```bash
sudo mkdir -p /var/lib/nomctl-relay
sudo chown 65532:65532 /var/lib/nomctl-relay
```

You can also use `deploy/relay/docker-compose.yml` or build locally from source with `docker build -f deploy/relay/Dockerfile -t nomctl-relay .`.

### Verify

Open `https://alerts.example.org/healthz` (expects `ok`), confirm `relay listening` in container logs, and send `/start` to your bot in Telegram.

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

## Upgrading

Pairings, mutes, and last-sent state live in `/data/relay.db` and survive upgrades; the SQLite schema initializes and migrates automatically on startup. Back up `/data/relay.db` before upgrading.

- **Coolify**: redeploy the resource to pull the new image.
- **Standalone Docker**: pull the new image and recreate the container against the same `/data` volume:

  ```bash
  docker pull ghcr.io/0x3639/nomctl-relay:latest
  docker stop nomctl-relay && docker rm nomctl-relay
  # rerun docker run with the same -v relay-data:/data and environment variables
  ```

- **Docker Compose**: `docker compose pull && docker compose up -d`.

## How it behaves

- Pairing codes are single use, expire after 10 minutes, and are rate limited per client address.
- Every node request is signed (HMAC-SHA256 with the node's secret, 5-minute replay window). Store or transport errors never turn into "unpaired"; only an unknown node id does.
- An alert is recorded as delivered only after Telegram accepted it; failed sends return an error to the node, which retries.

## What the relay accesses and stores

- **Container isolation**: runs as a static binary on `gcr.io/distroless/static:nonroot` (`uid 65532`, no shell) with only the `/data` volume writable.
- **Outbound network**: connects only to `https://api.telegram.org` using `RELAY_TELEGRAM_TOKEN` (the token is stripped from transport error logs).
- **SQLite database (`/data/relay.db`)**: stores pairing codes, Telegram `chat_id`, `node_id`, node `name`, `host`, `nomctl` version, 32-byte HMAC `secret`, `created`/`last_seen` timestamps, `silent`/`blocked` flags, the latest heartbeat `summary` JSON, `mutes`, and `last_sent` timestamps. Alert message text is forwarded and **not** stored; node IP addresses are **not** stored in the database.
- **Network visibility**: sees each paired node's public IP address in memory for per-IP rate limiting (and your reverse proxy may log client IPs).
- **Zero access to nodes**: communication is strictly one-way push from node to relay. The relay has no SSH, RPC, or inbound network access to any Zenon node.
