---
title: Orchestrator
description: "Install, upgrade, configure, apply Alchemy/dRPC sync templates, back up and copy sync data, check status, follow logs and hard reset the Zenon orchestrator."
---

The orchestrator is a separate service, usually installed next to a pillar's node. nomctl provides an **Orchestrator** management submenu under **Pillar & orchestrator** in the interactive menu, plus `nomctl orchestrator` subcommands for automation.

```bash
sudo nomctl orchestrator install                       # pick GitHub account & release (default: 0x3639/orchestrator, newest)
sudo nomctl orchestrator upgrade                       # upgrade installed orchestrator from a GitHub release
sudo nomctl orchestrator status                        # active / inactive / not found
sudo nomctl orchestrator logs -f                       # follow the journal, Ctrl+C to stop
sudo nomctl orchestrator backup                        # archive events/ and queues/ with SHA-256 sidecar
sudo nomctl orchestrator copy-sync --to root@peer      # rsync events/ and queues/ to another server
sudo nomctl orchestrator config show                   # print redacted config.json
sudo nomctl orchestrator config edit --restart         # edit config.json in $EDITOR with JSON validation
sudo nomctl orchestrator template list                 # show built-in Alchemy and dRPC sync templates
sudo nomctl orchestrator template apply --bnb drpc --eth alchemy --restart
sudo nomctl orchestrator hard-reset                    # stop, delete queues/ and events/, start
```

## Install and upgrade

Both `install` and `upgrade` let you select a GitHub account/repository (defaulting to `https://github.com/0x3639/orchestrator`) and list all published releases from that repository, defaulting to the most recent release. You can pass an account name (`HyperCore-Team`), an `owner/repo` slug, or a full GitHub URL via `--repo`.

1. Download the platform release archive (`orchestrator-linux-amd64.zip` or `orchestrator-linux-arm64.zip`) and verify its SHA-256 checksum before stopping anything. If an existing unit is installed, verify that its `ExecStart` path matches the configured install target (`/usr/local/bin/orchestrator` by default) and refuse on a mismatch.
2. Stop the orchestrator service (when installed).
3. Atomically install the verified binary to `/usr/local/bin/orchestrator` while keeping a staged copy of the prior binary on the same filesystem.
4. On `install`, create `NOMCTL_ORCHESTRATOR_DIR` (`/root/.orchestrator` by default, mode `0700` when newly created; existing directory permissions are preserved), a starter `config.json` (mode `0600`), and `/etc/systemd/system/orchestrator.service` (running as `root` with `Environment="HOME=<parent>"`, enabled with `Restart=always`) if absent. Because the orchestrator binary always reads `<HOME>/.orchestrator`, generating a new unit requires `NOMCTL_ORCHESTRATOR_DIR` to end in `/.orchestrator`. Existing units (including vendor units) and configuration are preserved.
5. Start or restart the orchestrator service and verify it is active. Only after startup succeeds is the prior binary promoted to `/usr/local/bin/orchestrator.previous`; if startup fails, the prior binary is restored and the previous service is restarted if it was running.

Non-interactive automation requires `--yes`:

```bash
sudo nomctl orchestrator install --yes
sudo nomctl orchestrator upgrade --repo HyperCore-Team --version v0.0.9a-alphanet --yes
```

## Back up and copy sync data

Orchestrator sync data lives in `events/` and `queues/` under `NOMCTL_ORCHESTRATOR_DIR`. Configuration (`config.json`), producer keys, TSS key shares (`tss/`), and voting history are never included in sync backups or cross-server sync transfers.

- **`nomctl orchestrator backup [--output DIR]`**: stops the orchestrator, captures `events/` and `queues/` into a private staging directory, restarts the orchestrator before compression, and writes `orchestrator_sync_<timestamp>.tar.gz` plus its `.hash` SHA-256 sidecar under `/backup/orchestrator` (or `--output`).
- **`nomctl orchestrator copy-sync --to HOST [--port 22] [--remote-dir PATH] [--remote-service NAME] [--sudo] [--yes]`**: verifies SSH and `rsync` on both hosts, stops the local orchestrator, transfers `events/` and `queues/` with `rsync` over SSH to a remote staging directory, stops the remote orchestrator (if present), moves the target's previous `events/` and `queues/` into a timestamped `sync-backup-before-copy-*` directory, swaps in the transferred sync data, and restarts active services.

## Edit `config.json` and sync templates

- **`nomctl orchestrator config show [--show-secrets]`**: prints `config.json`, redacting `ProducerKeyFilePassphrase` and secret RPC URL paths/queries unless `--show-secrets` is passed.
- **`nomctl orchestrator config edit [--restart]`**: opens a private copy of `config.json` in `$VISUAL`, `$EDITOR`, or `nano`. JSON syntax and orchestrator settings are validated before an atomic mode-`0600` save with a `config.json.bak.<unix>` backup.
- **`nomctl orchestrator template apply`**: updates `FilterQuerySize`, `RpcRequestsPerSecond`, and `RpcBurst` per EVM network while keeping `Urls` and all other configuration fields untouched. Each network can use its own provider template:

| Provider | Network | `FilterQuerySize` | `RpcRequestsPerSecond` | `RpcBurst` |
|---|---|---:|---:|---:|
| dRPC (`drpc.org`) | `BNB Chain` | `100` | `2` | `3` |
| dRPC (`drpc.org`) | `Ethereum` | `100` | `2` | `3` |
| Alchemy | `BNB Chain` | `10` | `0.5` | `1` |
| Alchemy | `Ethereum` | `10` | `0.5` | `1` |

## Hard reset

When the orchestrator is stuck, its queued work and event log are discarded and rebuilt from the chain:

1. Stop the orchestrator unit.
2. Wait ten seconds for it to settle.
3. Delete `queues/` and `events/` under the orchestrator directory. Everything else there, including its configuration, stays.
4. Start the unit again.

The menu confirms before starting. The command line does not, like `resync`. A directory that does not exist is skipped, and if a delete fails the orchestrator is still started again and the error reported.

## Settings

| Variable | Default | |
|---|---|---|
| `NOMCTL_ORCHESTRATOR_SERVICE` | `orchestrator` | systemd unit, without `.service` |
| `NOMCTL_ORCHESTRATOR_DIR` | `/root/.orchestrator` | state directory (`config.json`, `queues/`, `events/`) |
| `NOMCTL_ORCHESTRATOR_REPO` | `0x3639/orchestrator` | default GitHub repository for orchestrator releases |
| `NOMCTL_ORCHESTRATOR_BINARY` | `orchestrator` | binary name installed under `NOMCTL_INSTALL_DIR` |
