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
  -e RELAY_TRUSTED_PROXIES='<proxy-gateway-or-ip>/32' \
  -v relay-data:/data \
  ghcr.io/0x3639/nomctl-relay:latest
```

Set `RELAY_TRUSTED_PROXIES` to the source address the container actually sees from your reverse proxy:

- When a host-installed reverse proxy forwards to a published port (`-p 127.0.0.1:8080:8080`), the container typically sees the Docker bridge gateway IP (for example `172.17.0.1`), **not** `127.0.0.1`. Check the gateway with `docker inspect -f '{{range .NetworkSettings.Networks}}{{.Gateway}}{{end}}' nomctl-relay`.
- When the reverse proxy runs in another container on a shared Docker network, use that proxy container's IP (or the narrowest subnet containing only trusted proxies).
- Use `127.0.0.1/32` only when the relay uses host networking (`--network host`).

If the proxy's address is not in `RELAY_TRUSTED_PROXIES`, `X-Forwarded-For` is ignored and all proxied clients share one rate-limit bucket (10 pairing attempts/min and 120 unauthenticated admission requests/min). Keep the range as narrow as possible, since any peer inside `RELAY_TRUSTED_PROXIES` is trusted to set `X-Forwarded-For`.

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

`sudo nomctl alerts setup --relay https://alerts.example.org` (or `sudo NOMCTL_RELAY_URL=https://alerts.example.org nomctl alerts setup`, since `sudo` drops exported environment variables by default). To bake the URL into your own builds, set the `NOMCTL_RELAY_URL` repository variable (releases) or `make build RELAY_URL=...`.

## Upgrading

Pairings, mutes, and last-sent state live in `/data/relay.db` and survive compatible upgrades. On startup, the relay initializes missing tables (`CREATE TABLE IF NOT EXISTS`), but it does not track schema versions or alter existing tables automatically—check release notes before upgrading across breaking versions.

Because `/data/relay.db` is opened in SQLite WAL mode, recent writes (including newly paired nodes) can remain in `/data/relay.db-wal` until checkpointed. **Do not back up by copying `relay.db` alone while the relay is running.** Before upgrading, take a consistent SQLite snapshot using one of:

- Stop the relay first, then copy `relay.db`, `relay.db-wal`, and `relay.db-shm` together.
- Use SQLite's online backup (`sqlite3 /data/relay.db ".backup /path/to/relay-backup.db"`), which captures committed WAL content.

Then update the container:

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

- **Container isolation**: runs as a static binary on `gcr.io/distroless/static:nonroot` (`uid 65532`, no shell) and stores persistent state only under `/data`. The default Docker and Compose examples above do not set `--read-only` / `read_only: true`, so Docker's container writable layer remains writable unless you explicitly enable a read-only root filesystem.
- **Outbound network**: connects to `https://api.telegram.org` by default using `RELAY_TELEGRAM_TOKEN` (the token is stripped from transport error logs). A test-only `RELAY_TELEGRAM_URL` environment variable can override the Bot API base URL; when set, all Bot API requests—including the bot token in the request URL path—are sent to that configured host, so leave it unset in production.
- **SQLite database (`/data/relay.db`)**: stores pairing codes, Telegram `chat_id`, `node_id`, node `name`, `host`, `nomctl` version, 32-byte HMAC `secret`, `created`/`last_seen` timestamps, `silent`/`blocked` flags, the latest heartbeat `summary` JSON, `mutes`, and `last_sent` timestamps. Alert message text is forwarded and **not** stored; node IP addresses are **not** stored in the database.
- **Network visibility**: sees each paired node's public IP address in memory for per-IP rate limiting (and your reverse proxy may log client IPs).
- **Zero access to nodes**: communication is strictly one-way push from node to relay. The relay has no SSH, RPC, or inbound network access to any Zenon node.
