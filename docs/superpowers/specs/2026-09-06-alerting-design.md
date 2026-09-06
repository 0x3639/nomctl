# Alerting: Telegram alerts through a shared relay

Status: approved design, spec for review. Target release: v0.3.0.

## Goals

1. Tell the operator, on Telegram, when their node needs attention and
   again when it recovers.
2. One shared Telegram bot for all operators; each operator sees only
   alerts for the nodes they paired. Pairing takes under a minute and needs
   no BotFather step.
3. Detect a node that has gone dark (power, network), which the node can
   never report about itself.
4. Keep the bot token off every node.

## Non-goals

- Two-way commands from Telegram into a node (`/status` asking the node).
  The relay never sends anything to a node; nodes only push.
- Other channels (Discord, generic webhook). The transport is behind an
  interface so they can be added later on the node side or as relay
  outputs.
- Storing alert history on the relay beyond last-sent timestamps.

## Components

```
+------------------+   HTTPS (Coolify proxy)   +------------------+   Bot API    +----------+
| node: nomctl     | ------------------------> | nomctl-relay     | -----------> | Telegram |
| alerts run       |  /v1/pair /v1/alert       | (container,      |  sendMessage |          |
| (systemd service)|  /v1/heartbeat            |  SQLite volume)  | <----------- |          |
+------------------+                           +------------------+  getUpdates  +----------+
```

- `nomctl alerts ...`: node-side commands and the daemon (`internal/alerts`).
- `nomctl-relay`: the server (`cmd/nomctl-relay`, `internal/relay`).
- `internal/alertproto`: request/response types and request signing shared
  by both sides.

## Pairing flow

1. Operator opens the bot in Telegram and presses Start (or sends `/start`).
2. Relay creates a pairing code: 8 characters from an unambiguous alphabet
   (no 0/O/1/I), valid 10 minutes, single use, bound to that chat id. Reply:
   "Your pairing code is `AB3K7QWX`. On the node run: sudo nomctl alerts
   setup". A chat can hold at most 5 unused codes.
3. On the node, `sudo nomctl alerts setup [--code X] [--name N] [--relay URL]`
   prompts for the code (unless given), prompts for the node name with the
   hostname as default (unless `--name`), then `POST /v1/pair`.
4. Relay validates the code, creates the node record `{id, chat_id, name,
   host, secret, created, last_seen}`, returns `{node_id, secret}`. Names
   must be unique per chat; the relay rejects a duplicate with a clear
   message and the node re-prompts.
5. Node writes `/etc/nomctl/alerts.json` (0600), writes and enables
   `nomctl-alerts.service`, starts it, and sends a `test` alert. The chat
   receives "Node pillar-1 paired and reporting."

## Protocol (`internal/alertproto`)

All node requests are JSON bodies with these headers:

- `X-Nomctl-Node`: node id
- `X-Nomctl-Timestamp`: unix seconds
- `X-Nomctl-Signature`: hex HMAC-SHA256(secret, timestamp + "\n" + body)

The relay rejects timestamps more than 5 minutes off and signatures that
do not verify. Pairing is the only unauthenticated node request and is
rate limited per source IP (10 per minute).

| Endpoint | Body | Response |
|---|---|---|
| `POST /v1/pair` | `{code, name, host, version}` | `{node_id, secret, relay_version}` |
| `POST /v1/alert` | `{alert, state:"firing"\|"ok"\|"info", title, detail, at}` | 204 |
| `POST /v1/heartbeat` | `{at, summary:{state, height, peers, restarts}}` | 204 |
| `POST /v1/unpair` | `{}` | 204 |
| `GET /healthz` | | `ok` |

`summary` in the heartbeat is what `/nodes` shows in Telegram; it is the
only node data the relay keeps, overwritten on every heartbeat.

## Relay (`nomctl-relay`)

**Configuration**, environment only:

| Variable | Default | Meaning |
|---|---|---|
| `RELAY_TELEGRAM_TOKEN` | required | bot token from BotFather |
| `RELAY_DB` | `/data/relay.db` | SQLite path (persistent volume) |
| `RELAY_LISTEN` | `:8080` | HTTP listen address |
| `RELAY_SILENT_AFTER` | `5m` | heartbeat gap before `node_silent` |
| `RELAY_PUBLIC_URL` | empty | shown in `/start` text if set |
| `RELAY_LOG_LEVEL` | `info` | |

**Storage**: interface `Store` with `CreateCode`, `ConsumeCode`, `CreateNode`,
`GetNode`, `ListNodes(chat)`, `TouchNode`, `DeleteNode`, `GetMute`, `SetMute`,
`LastSent`, `SetLastSent`. SQLite implementation using
`modernc.org/sqlite` (pure Go, keeps CGO off). Schema in one migration.

**Telegram**: long polling `getUpdates` in a goroutine; `sendMessage` with
Markdown-V2 escaped text; retries with backoff on 429/5xx. Commands:

| Command | Effect |
|---|---|
| `/start` | issue a pairing code |
| `/nodes` | list this chat's nodes: name, host, last heartbeat age, last summary |
| `/unpair <name>` | delete the node record (the node's next request gets 401 and its daemon logs it) |
| `/mute <name> <alert\|all> [duration]` | suppress delivery (default 24h) |
| `/unmute <name> <alert\|all>` | |
| `/help` | |

**Delivery rules**: an alert is delivered when its state differs from the
last delivered state for that (node, alert), or when 10 minutes have passed
since the last delivery of a still-firing alert (a reminder), unless muted.
`info` alerts (test, paired) are always delivered. Message format:

```
🔴 pillar-1 · service down
go-zenon inactive (dead), 3 restarts
host node1 · 2026-09-06 12:00 UTC
```

with 🟢 for `ok`, 🟠 for warnings (`disk_low`, `memory_high`, `fds_high`,
`backup_stale`) and ℹ️ for info.

**node_silent**: a ticker every 30 s finds nodes whose `last_seen` is
older than `RELAY_SILENT_AFTER` and have not been flagged; sends
`node_silent` and flags them. A heartbeat from a flagged node sends
`node_silent_ok` and clears the flag.

**Limits**: 60 requests per minute per node, 20 messages per minute per
chat (excess coalesced into one "N alerts suppressed" line), request body
capped at 64 KiB.

**Container**: `deploy/relay/Dockerfile` (scratch base, static binary,
non-root uid 65532, `/data` volume, `EXPOSE 8080`, `HEALTHCHECK` hitting
`/healthz`), `deploy/relay/docker-compose.yml` with the env vars and volume,
`deploy/relay/README.md` with Coolify steps: create the bot with BotFather,
add a Docker Image or Compose resource, set `RELAY_TELEGRAM_TOKEN` as a
secret, mount a volume at `/data`, attach the domain, health check
`/healthz`. goreleaser publishes `ghcr.io/0x3639/nomctl-relay:<version>` and
`:latest`.

## Node agent (`internal/alerts`)

**Config** `/etc/nomctl/alerts.json` (0600):

```json
{
  "relay_url": "https://alerts.example.org",
  "node_id": "n_7f3a...",
  "secret": "base64...",
  "name": "pillar-1",
  "interval": "30s",
  "alerts": {
    "service_down": {"enabled": true},
    "disk_low": {"enabled": true, "min_free_gb": 15},
    ...
  }
}
```

`NOMCTL_RELAY_URL` and `--relay` override the default relay URL at setup
time; the default is a compile-time constant set via ldflags
(`-X .../internal/alerts.DefaultRelayURL=...`) so a fork can point at its
own relay.

**Commands**:

| Command | Effect |
|---|---|
| `nomctl alerts setup [--code] [--name] [--relay]` | pair, write config, install and start `nomctl-alerts.service`, send test |
| `nomctl alerts run` | the daemon (used by the unit; can be run in the foreground for debugging) |
| `nomctl alerts status` | paired chat/node name, service state, each alert's state and last change (read from the daemon's state file `/run/nomctl/alerts-state.json`) |
| `nomctl alerts list` | alerts with enabled flag and thresholds |
| `nomctl alerts enable\|disable <alert>` | edit config, signal the daemon (SIGHUP reloads config) |
| `nomctl alerts set <alert>.<key> <value>` | change a threshold, validated per alert |
| `nomctl alerts test` | send an info alert through the daemon's config |
| `nomctl alerts unpair` | tell the relay, stop and disable the service, delete the config |

All require root; `setup` and `status` are diagnostic (no pre-flight).
The menu gets `alerts → Set up Telegram alerts` which runs setup or, if
already paired, shows status.

**Daemon loop** (`run`): every `interval` take a `metrics.Sample`, evaluate
every enabled rule, and for each rule whose state changed send an alert;
send a heartbeat with the summary on every iteration; on HTTP failure log
and retry next iteration (no local queue; the relay's `node_silent` covers
a long outage). Reload config on SIGHUP. Write the state file after every
evaluation. Exit cleanly on SIGTERM.

**Rules** are pure functions `func(history []metrics.Sample, cfg RuleConfig) (firing bool, detail string)` over the last N samples, so "for 2 consecutive samples" and "for 5 minutes" are expressed over the history window (kept at 30 minutes):

| Alert | Severity | Fires when | Default thresholds |
|---|---|---|---|
| `service_down` | critical | unit not active for 2 consecutive samples | |
| `crash_loop` | critical | NRestarts increased at least twice within the window | window 10m, count 2 |
| `sync_stalled` | critical | Node.Stalled true for 2 consecutive samples | (StalledAfter 2m from metrics) |
| `sync_behind` | warning | syncing and (target − current) has not decreased across 10 minutes | 10m |
| `not_enough_peers` | warning | state NotEnoughPeers or peers < min for 5 minutes | min_peers 3, 5m |
| `disk_low` | warning | data dir free < min | min_free_gb 15 |
| `memory_high` | warning | RSS > pct of MemTotal | pct 85 |
| `fds_high` | warning | open files > pct of limit | pct 80 |
| `backup_stale` | warning | `nomctl-backup.timer` enabled and newest archive older than cadence + 1 day | |
| `rpc_unreachable` | warning | service active but node RPC unreachable for 5 minutes | 5m |

Every rule has an `_ok` message when it stops firing. Rule evaluation
never sends on the first evaluation after start (no "ok" storm on boot);
the daemon sends a single `info` "alerts started" instead.

**State file** `/run/nomctl/alerts-state.json`: per alert `{firing, since,
last_sent}` plus `last_heartbeat_ok`, read by `nomctl alerts status`.

## Security

- Bot token only on the relay, provided as an environment secret.
- Node secret 32 random bytes, stored 0600, never logged; requests signed;
  replay window 5 minutes.
- Pairing codes single use, 10 minute expiry, rate limited.
- The relay stores: chat id, node name, host name, last heartbeat summary,
  mute settings, last-sent times. No alert text is retained.
- Relay container runs as non-root on a scratch image; no shell.
- Unpairing from either side invalidates the secret immediately.

## Error handling

- Relay unreachable at setup: clear error with the URL; nothing installed.
- Relay unreachable at runtime: daemon logs at warn once per 10 minutes,
  keeps sampling; alerts are not queued (the relay raises `node_silent`).
- 401 from the relay (unpaired remotely): daemon logs an error, stops
  sending, and `nomctl alerts status` shows "unpaired by the operator; run
  setup again".
- Telegram API errors: retried with backoff; a chat that blocked the bot
  (403) has its nodes marked `blocked` and delivery skipped until `/start`
  again.

## Testing

- `internal/alertproto`: signing round trip, tampered body, stale timestamp.
- `internal/alerts/rules`: table tests per rule with synthetic sample
  histories (fires, does not fire, recovers, hysteresis).
- `internal/alerts` daemon loop with a fake relay server: transitions
  produce exactly one request each, reminders after 10 minutes, no storm
  at start, SIGHUP reload.
- `internal/relay`: handlers with httptest and the in-memory store; pairing
  code lifecycle; HMAC verification; delivery rules and mutes; node_silent
  ticker with a fake clock; Telegram command parsing against a fake Bot API
  server; SQLite store passes the same suite as the in-memory store.
- End-to-end: relay in-process with fake Telegram, node agent pairs and
  fires `service_down` then `_ok`, assert two Telegram messages to the
  right chat and none to another chat.

## README additions

"Alerts" section: create the bot once (for the relay operator), operator
pairing steps with screenshots-free text, the alert table, mute/unmute,
and the relay deployment guide link. Roadmap item 1 marked done at release.
