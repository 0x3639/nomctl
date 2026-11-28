---
title: Pairing and deploying alerts
description: "Step-by-step setup and upgrade of the nomctl alerts daemon on a node, plus full disclosure of what it accesses."
---

## 1. Install or update nomctl on the node

If nomctl is not installed yet (Debian/Ubuntu, `amd64` or `arm64`):

```bash
curl -fsSL https://raw.githubusercontent.com/0x3639/nomctl/main/install.sh | sudo bash
```

If it is already installed, update it first with `sudo nomctl upgrade`.

## 2. Get a pairing code

Open the nomctl alerts bot in Telegram (or your own bot if you [run your own relay](/alerts/relay)) and send `/start`. It replies with the privacy notice below and an 8-character pairing code, valid for 10 minutes and usable once. You can hold up to five unused codes.

:::warning[Privacy notice: your node's IP address is visible to the relay]

Once paired, your node sends a heartbeat to **alerts.zenon.info** every 30 seconds and forwards alerts through it. The relay operator can see your node's **public IP address**, host name, node name and status summary. The IP address is used only to rate-limit pairing and is not stored in the relay database, but it is visible to the relay and may appear in the logs of the proxy in front of it.

If your pillar's IP address must stay private, [run your own relay](/alerts/relay) and point nodes at it with `--relay` (or `sudo NOMCTL_RELAY_URL=...`), or do not pair.

:::

## 3. Pair and start the alerts daemon

`nomctl alerts setup` prints the same notice and, on a terminal, asks you to confirm before anything is sent to the relay. Pass `--accept-privacy-notice` to skip the question in scripts; the notice is still printed.

On the node:

```bash
sudo nomctl alerts setup
```

`setup` asks for the code and a **name** for the node (default: the hostname), then:

1. Redeems the code with the relay (`POST /v1/pair`) and receives a `node_id` and 32-byte HMAC secret.
2. Checks the local node RPC (`embedded.pillar.getByName`) to see whether the node name matches a registered Pillar.
3. Writes `/etc/nomctl/alerts.json` (`root:nomctl`, mode `0640`).
4. Creates the locked system user `nomctl`, installs and enables `nomctl-alerts.service`, `nomctl-alerts-probe.service`, and `nomctl-alerts-probe.timer`, and starts them.
5. Sends a test alert so your chat receives "paired and reporting" followed by "test alert".

Flags skip the prompts, which is how to script it:

```bash
sudo nomctl alerts setup --code AB3K7QWX --name pillar-1 --accept-privacy-notice
```

Released binaries default to the community relay at `https://alerts.zenon.info`. Pass `--relay https://alerts.example.org` to use your own relay. Setting `NOMCTL_RELAY_URL` in your shell does not work with `sudo` under its default `env_reset` policy unless passed through explicitly (for example, `sudo --preserve-env=NOMCTL_RELAY_URL nomctl alerts setup` or `sudo NOMCTL_RELAY_URL=https://alerts.example.org nomctl alerts setup`).

## 4. Verify and customize

Check the local daemon state and confirm the live card in Telegram with `/nodes`:

```bash
sudo nomctl alerts status                 # pairing, service user, probe timer, last heartbeat, per-alert state
sudo nomctl alerts list                   # alerts and thresholds
```

### The name is also the pillar name

If a registered pillar has the same name as the node, setup says "monitoring pillar NAME (rank N)", stores it, and the `pillar_missed` alert becomes active. `nomctl status` and `top` then show the pillar's rank and produced versus expected momentums for the epoch.

If the names differ, set it afterwards; the value is checked against the pillar list:

```bash
sudo nomctl alerts set pillar.name MyPillar
sudo nomctl alerts set pillar.name ""      # clear
```

### More than one node

Pair as many nodes as you like to the same chat. Names must be unique per chat, and every message starts with the node's name.

### Managing on the node

```bash
sudo nomctl alerts set disk_low.min_free_gb 30
sudo nomctl alerts disable backup_stale
sudo nomctl alerts enable backup_stale
sudo nomctl alerts test
sudo nomctl alerts unpair                 # keeps credentials if the relay is unreachable; --force removes them anyway
```

Threshold and switch changes reach the running daemon immediately via `SIGHUP`. Duration thresholds are limited to 30 minutes, the amount of history the daemon keeps.

## Upgrading the alerts daemon

The alerts daemon and probe are subcommands of the `nomctl` binary (`nomctl alerts run` and `nomctl alerts probe`), so upgrading `nomctl` upgrades alerting too:

```bash
sudo nomctl upgrade
```

`upgrade` verifies the new release against `checksums.txt`, atomically replaces `/usr/local/bin/nomctl` (keeping `nomctl.previous` for `sudo nomctl upgrade --rollback`), reconciles the `nomctl` user, file permissions, and all three systemd units (`alerts.Converge`), and restarts `nomctl-alerts.service`.

Nodes set up on releases before 0.9.0 ran the daemon as root. They migrate automatically to the unprivileged `nomctl` user and root probe layout on their next `sudo nomctl upgrade` (or whenever `nomctl alerts setup`, `enable`, `disable`, or `set` runs), with no manual step. Existing runtime files are not recursively reassigned to another user; old daemon state is replaced on the next sample.

## What runs on the node

Setup installs three systemd units:

| Unit | Runs as | Network | Purpose |
|---|---|---|---|
| `nomctl-alerts.service` | `nomctl` (locked system user, no capabilities) | Local HTTP (`znnd` RPC) and outbound HTTPS (relay, GitHub); no listening ports | Evaluates rules every 30 s, keeps 30 min of history, sends signed heartbeats and alerts |
| `nomctl-alerts-probe.service` | `root` (oneshot) | No direct host IP network (`PrivateNetwork=true`) | Records open file count, I/O bytes, data-dir disk space, and local RPC URL to `/run/nomctl-system/process-probe.json` |
| `nomctl-alerts-probe.timer` | — | — | Runs `nomctl-alerts-probe.service` every 30 s |

### Why privileges are split

The alerts daemon talks to the internet every 30 seconds from the machine that holds the pillar's wallet, so it does not run as root. Counting another user's open files needs `CAP_SYS_PTRACE`, which would also allow reading `znnd`'s process memory. Rather than grant that to a network-facing daemon, a separate root probe with `PrivateNetwork=true` collects those counters and writes a small JSON file every 30 seconds for the daemon to read. `nomctl alerts status` shows the user the daemon runs as and whether the probe timer is installed.

### Systems `nomctl-alerts.service` accesses

The daemon unit runs with `NoNewPrivileges=true`, `CapabilityBoundingSet=` (empty), `AmbientCapabilities=` (empty), `ProtectSystem=strict`, `ProtectHome=true`, `PrivateTmp=true`, `ProtectKernelTunables=true`, `ProtectKernelModules=true`, `ProtectControlGroups=true`, `RestrictSUIDSGID=true`, `LockPersonality=true`, `RestrictRealtime=true`, and `ReadWritePaths=/run/nomctl`.

- **Local files**:
  - Reads `/etc/nomctl/alerts.json` (`root:nomctl`, mode `0640`) for relay credentials, node/pillar name, interval, and rule settings.
  - Reads `/run/nomctl-system/process-probe.json` (root-owned, mode `0644`; the daemon cannot modify it).
  - Writes `/run/nomctl/alerts-state.json` and `/run/nomctl/update-check.json`.
  - Reads backup schedule/metadata for `backup_stale`: `/etc/cron.d/nomctl-backup` (or legacy `/etc/systemd/system/nomctl-backup.service`) and archive filenames, timestamps, and `.hash` existence in `NOMCTL_BACKUP_DIR` (default `/backup`). It does not read archive contents.
- **Systemd and kernel telemetry (read-only)**:
  - Runs `systemctl show <service>` (active/sub state, result, cgroup, PID, restart count, start timestamp) and `systemctl is-enabled --quiet nomctl-backup.timer`.
  - Reads `/proc/<pid>/{status,stat,limits}`, `/proc/{loadavg,meminfo}`, `/proc/pressure/{cpu,io,memory}`, `/proc/self/mountinfo`, and `/sys/fs/cgroup/<unit>/{memory.current,memory.peak,memory.max,pids.current}`.
- **Local `znnd` JSON-RPC (read-only HTTP)** at the loopback URL published by the probe:
  - Calls only `stats.syncInfo`, `stats.networkInfo`, `stats.processInfo`, `stats.osInfo`, `ledger.getFrontierMomentum`, and (when a pillar name is configured) `embedded.pillar.getByName`. It never calls wallet, signing, or state-changing RPCs.
- **Outbound network (no inbound ports)**:
  - **Relay** (`https://alerts.zenon.info` or your configured relay): `POST /v1/heartbeat` every 30 s and `POST /v1/alert` on state transitions or 10-minute reminders, signed with HMAC-SHA256.
  - **GitHub (at most once per hour when `NOMCTL_UPDATE_CHECK=true`)**: `HEAD https://github.com/<NOMCTL_REPO>/releases/latest` and `git ls-remote <NOMCTL_REPO_URL> refs/heads/<NOMCTL_BRANCH_NAME>` to populate update indicators on heartbeats and the optional `update_available` rule. Because `nomctl-alerts.service` defaults to `NOMCTL_UPDATE_CHECK=true`, add a systemd drop-in with `Environment=NOMCTL_UPDATE_CHECK=false` (for example, via `sudo systemctl edit nomctl-alerts.service`), then reload systemd and restart the service (`sudo systemctl daemon-reload && sudo systemctl restart nomctl-alerts.service`) so an already-running daemon picks up the new environment.

### Systems `nomctl-alerts-probe.service` accesses

The probe unit runs as `root` with `PrivateNetwork=true`, `NoNewPrivileges=true`, `ProtectSystem=strict`, `ProtectHome=read-only`, `PrivateTmp=true`, and `ReadWritePaths=/run/nomctl-system`.

- Runs `systemctl show <service>` to read `MainPID`.
- Reads `/proc/<pid>/{stat,fd,limits,io}` (file descriptor count, limit, and I/O byte counters only).
- Measures filesystem free/total space (`statfs`) on `NOMCTL_ZNN_DIR` (so data directories under `/root` or on separate mounts are measured accurately despite `ProtectHome=true` on the daemon).
- Reads only `RPC.EnableHTTP`, `RPC.HTTPHost`, and `RPC.HTTPPort` from `<NOMCTL_ZNN_DIR>/config.json` and publishes only the resolved local HTTP URL—never producer keys, passwords, or other config fields—to `/run/nomctl-system/process-probe.json`.

### What leaves the node

Every 30-second heartbeat carries the node's status summary used by `/nodes`: sync state, height, target height, momentum rate, ETA, frontier height/age (or `ledger_busy`), connected peer count, restart count, service uptime, `znnd` version/commit, `nomctl` version, pillar name/rank/produced/expected (if configured), host load/memory/disk free/total, `znnd` CPU/RSS, and update availability flags. Alert requests carry the alert name, severity, state (`firing`/`ok`/`info`), title, detail line, and timestamp.

### What it cannot access

- **`nomctl-alerts.service` cannot read `/root` or `/home` (`ProtectHome=true`), root-only files such as mode-`0600` `config.json` or wallet/producer keys under protected or root-owned paths, or `znnd` process memory** (unprivileged `nomctl` user, zero capabilities). Its writable paths are limited to `/run/nomctl` and its private `/tmp` and `/var/tmp` directories (`PrivateTmp=true`); it cannot modify `/etc`, `/usr`, `/backup`, or `/run/nomctl-system`, cannot start/stop `go-zenon.service`, never holds the Telegram bot token, and accepts no inbound commands from the relay.
- **`nomctl-alerts-probe.service` has no direct host IP network access** (`PrivateNetwork=true`; local filesystem Unix sockets remain accessible, which `systemctl show` uses), and its writable paths are limited to `/run/nomctl-system` plus its private `/tmp` and `/var/tmp` directories (`PrivateTmp=true`). Because it runs as `root` with `ProtectHome=read-only`, `/root` is visible and read-only to the process, though the probe itself only reads RPC settings from `config.json` and disk/process counters and does not read `/root/.znn/wallet/`, producer keys/passwords, or chain database contents.
- Because `ProtectHome=true` hides `/root/.znn` from the unprivileged daemon, the opt-in `wallet_backup_missing` rule cannot read a producer key or `config.json` located under `/root/.znn`; it only applies when `NOMCTL_ZNN_DIR` lives outside protected home directories and its `config.json` is readable by `nomctl` (keep `config.json` root-only `0600` when it holds a producer password).

## If something fails during setup

If pairing succeeds but the service cannot be installed, setup undoes the pairing at the relay so you do not get a `node silent` alert for a node that never reported. If even that fails, the credentials are kept and setup tells you to run `nomctl alerts unpair`.
