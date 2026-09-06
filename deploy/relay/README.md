# nomctl-relay deployment

The relay is one container with one SQLite file. It holds the Telegram bot
token; nodes never see it.

## 1. Create the bot

1. Open Telegram, talk to `@BotFather`, send `/newbot`, pick a name and a
   username. Save the token it prints.
2. Optional: `/setdescription` and `/setcommands` with:

   ```
   start - get a pairing code for a new node
   nodes - list your paired nodes
   mute - silence an alert: /mute NAME ALERT [DURATION]
   unmute - resume an alert: /unmute NAME ALERT
   unpair - forget a node: /unpair NAME
   help - show help
   ```

## 2. Deploy on Coolify

Either add a **Docker Image** resource with image
`ghcr.io/0x3639/nomctl-relay:latest`, or a **Docker Compose** resource
pasting `docker-compose.yml` from this directory. Then:

- **Environment**: `RELAY_TELEGRAM_TOKEN` (mark it as a secret),
  `RELAY_PUBLIC_URL=https://alerts.example.org` (shown to operators),
  optionally `RELAY_SILENT_AFTER` (default `5m`).
- **Storage**: a persistent volume mounted at `/data`. This is the only
  state; back up `/data/relay.db`.
- **Network**: expose container port 8080 and attach your domain. Coolify's
  proxy terminates HTTPS. No inbound webhook from Telegram is needed: the
  relay long-polls.
- **Health check**: HTTP `GET /healthz` on port 8080, or the image's built-in
  `HEALTHCHECK`.

Environment variables:

| Variable | Default | Meaning |
|---|---|---|
| `RELAY_TELEGRAM_TOKEN` | required | bot token from BotFather |
| `RELAY_DB` | `/data/relay.db` | SQLite path |
| `RELAY_LISTEN` | `:8080` | HTTP listen address |
| `RELAY_SILENT_AFTER` | `5m` | heartbeat gap before `node_silent` fires |
| `RELAY_PUBLIC_URL` | empty | shown in the `/start` reply |
| `RELAY_LOG_LEVEL` | `info` | `info` or `debug` |

## 3. Point nomctl at it

Nodes pair with `sudo nomctl alerts setup --relay https://alerts.example.org`
(or `NOMCTL_RELAY_URL`). To bake the URL into the binary, build with
`-ldflags "-X github.com/0x3639/nomctl/internal/alerts.DefaultRelayURL=https://alerts.example.org"`;
the release workflow does this from the `NOMCTL_RELAY_URL` repository
variable.

## Upgrading

Pull the new image and restart; the schema migrates itself. Pairings,
mutes and last-sent state live in the volume and survive upgrades.

## Local build

```bash
docker build -f deploy/relay/Dockerfile -t nomctl-relay .
docker run --rm -p 8080:8080 -e RELAY_TELEGRAM_TOKEN=... -v relay-data:/data nomctl-relay
```
