---
title: Commands
description: "Every nomctl subcommand and flag."
---

Every menu action is a subcommand. All require root except `--help`, `--version`, `env` and `completion`.

## Global flags

| Flag | Effect |
|---|---|
| `--debug` | verbose logging; external command output shown on the terminal instead of hidden behind spinners |
| `--log-file PATH` | log file (default `/var/log/nomctl.log`) |
| `--skip-preflight` | skip the CPU/RAM/NTP/Internet checks |

## Node lifecycle

| Command | Notes |
|---|---|
| `nomctl deploy [--repo URL] [--branch NAME]` | [Deploy](/guide/deploy) |
| `nomctl upgrade-node [--repo URL] [--branch NAME] [--restore-point-keep DAYS_OR_indefinite] [--yes]` | [Source upgrades](/guide/upgrading#source-upgrades) (default); builds before downtime, then creates a protected restore point and installs |
| `nomctl upgrade-node --signed [--version TAG] [--restore-point-keep DAYS_OR_indefinite] [--yes]` | [Signed node releases](/guide/upgrading#signed-node-releases); `--version` requires `--signed`, which cannot be combined with `--repo` or `--branch` |
| `nomctl start` / `stop` / `restart` | [Service control](/guide/service) |
| `nomctl logs [-f] [-n LINES]` | last 20 lines, or follow |
| `nomctl resync` | wipe chain data, keep wallet and config |
| `nomctl backup [--retention-days DAYS] [--max-backups N] [--cadence DAYS] [--hour HOUR] [--schedule]` | [Backups](/guide/backups) |
| `nomctl backup point [--keep DAYS_OR_indefinite] [--label TEXT]` | Back up chain data and znnd; record its version and hashes in a protected restore point |
| `nomctl restore point ID [--data-only]` | Without flags, interactive use asks whether to revert znnd and non-interactive use restores the matching pair. `--data-only` keeps installed znnd and skips all prompts, even in a terminal |
| `nomctl restore-points` | Manage protected points in the UI, or list them non-interactively |
| `nomctl backup manage` | Review and prune ordinary backups in the UI |
| `nomctl backup schedule [--retention-days DAYS] [--hour HOUR] [--directory PATH]` | Configure daily local backups with cron without taking a backup immediately |
| `nomctl backup status [--json]` | Show saved schedule, timezone, next slot, last scheduled attempt/success, and missed or failed runs |
| `nomctl backup schedule edit [--hour HOUR] [--retention-days DAYS] [--directory PATH]` | Change only supplied settings; preserve a paused schedule |
| `nomctl backup schedule pause` | Pause future daily backups; keep all stored files |
| `nomctl backup schedule resume` | Resume at the next daily slot after checking cron |
| `nomctl verify [--all \| --file FILE \| --point ID] [--directory PATH] [--json] [--rehearse]` | [Verify recovery files](/guide/backups#verify-recovery-files) without stopping znnd; defaults to all files; rehearsal checks temporary space first |
| `nomctl verify status [--all \| --file FILE \| --point ID] [--directory PATH] [--json]` | View saved verification results; recheck content identities and mark changed files stale |
| `nomctl verify schedule [--hour HOUR] [--directory PATH]` | Set up or edit daily verification without running it now; preserves a paused schedule |
| `nomctl verify schedule pause` / `nomctl verify schedule resume` | Control future verification; retain recovery files and saved results |
| `nomctl restore [--file FILE]` | Restore chain data only, keeping installed znnd; picker when `--file` is omitted |
| `nomctl backup wallet [--output DIR] [--no-encrypt]` | [wallet and config backup](/guide/backups#wallet-and-config-backup), age-encrypted; passphrase prompted or `NOMCTL_WALLET_PASSPHRASE` |
| `nomctl restore wallet FILE [--restart]` | verifies the archived key against its config before replacing anything |
| `nomctl bootstrap [URL] [--discard]` | [Bootstrap](/guide/bootstrap): verified snapshot instead of syncing from genesis |
| `nomctl analytics install` | [Analytics stack](/guide/analytics) |
| `nomctl upgrade [--check] [--version TAG] [--rollback]` | [Upgrading](/guide/upgrading) |

## Health and device migration

| Command | Notes |
|---|---|
| `nomctl health [--wait 30s] [--json]` | [Observe health](/guide/health) and intended znnd identity; no mutations |
| `nomctl migrate` | Open the [migration menu](/guide/migration) |
| `nomctl migrate plan --to HOST [--port 22] [--sudo] [--remote-nomctl PATH] [--destination-data PATH] [--destination-binary PATH] [--destination-service NAME]` | Save a checked preview; default target `/root/.znn`, `/usr/local/bin/znnd`, `go-zenon` |
| `nomctl migrate transfer [--yes]` / `nomctl migrate resume [--yes]` | Preserve recovery files, stop/block both nodes, then copy and verify; initial run requires a wallet-backup passphrase |
| `nomctl migrate status [--json]` | Saved phases plus current stopped/start-block evidence from both hosts |
| `nomctl migrate handoff [--yes]` | Explicitly allow the replacement to start and check health; keep the original blocked |
| `nomctl migrate return [--yes]` | Stop/block replacement before restarting and checking original |
| `nomctl migrate cancel [--yes]` | Discard only an untouched preview |

## config.json

| Command | Notes |
|---|---|
| `nomctl config show [--show-secrets]` | [config.json editor](/guide/config) |
| `nomctl config get KEY` | |
| `nomctl config set KEY VALUE [--restart] [--force]` | validated; previous file backed up |
| `nomctl config unset KEY [--restart]` | back to the default |
| `nomctl config edit [--restart]` | `$EDITOR`, validated before install |

## Pillar

| Command | Notes |
|---|---|
| `nomctl pillar deploy [--yes] [--password P]` | [Deploy a Pillar](/guide/pillar): official go-zenon master + service + producer key, in one |
| `nomctl pillar setup [--yes] [--password P]` (or `NOMCTL_PRODUCER_PASSWORD`) | [Pillar producer](/guide/pillar): create or configure the producer key, write `config.json`, restart the node |
| `nomctl pillar status [--show-password]` | producer address, key file, which Pillar uses it |

## Orchestrator

| Command | Notes |
|---|---|
| `nomctl orchestrator install [--repo REPO] [--version TAG] [--yes]` | [Orchestrator](/guide/orchestrator): download verified release (default `0x3639/orchestrator`, newest), stop, install `/usr/local/bin/orchestrator`, create unit/config if absent, start |
| `nomctl orchestrator upgrade [--repo REPO] [--version TAG] [--yes]` | upgrade an installed orchestrator from a verified GitHub release, keeping `.previous` |
| `nomctl orchestrator recover` | complete a pending orchestrator rollback or `.previous` publication recorded in `<binary>.pending.json` |
| `nomctl orchestrator status` | refuses when the unit is not installed |
| `nomctl orchestrator logs [-f] [-n 20]` | |
| `nomctl orchestrator backup [--output DIR]` | stop, archive `events/` and `queues/` with SHA-256 sidecar, restart |
| `nomctl orchestrator copy-sync --to HOST [--port 22] [--remote-dir PATH] [--remote-service NAME] [--sudo] [--yes]` | copy `events/` and `queues/` over SSH via `rsync` with a remote safety backup |
| `nomctl orchestrator config show [--show-secrets]` | print orchestrator `config.json` (secrets redacted by default) |
| `nomctl orchestrator config edit [--restart]` | `$EDITOR`, validated before atomic save with backup |
| `nomctl orchestrator template list` | list built-in Alchemy and dRPC (`drpc.org`) sync templates |
| `nomctl orchestrator template apply [--provider alchemy\|drpc] [--network bnb\|ethereum\|all] [--bnb PROVIDER] [--eth PROVIDER] [--restart]` | apply per-network sync settings while preserving `Urls` and unrelated config |
| `nomctl orchestrator hard-reset` | [Orchestrator](/guide/orchestrator): stop, delete `queues/` and `events/`, start |

## Observability

| Command | Notes |
|---|---|
| `nomctl doctor [--json] [--wait 10s] [--probe-url HTTPS_URL --public-ip IP]` | [Check my node](/troubleshooting/doctor): read-only diagnostics, suggested actions, znnd version/commit and installed/running SHA-256; external probe is opt-in |
| `nomctl status [--json] [--wait 2s] [--no-update-check]` | one-screen summary, with update lines when something is newer |
| `nomctl top [--interval 2s] [--no-update-check]` | live dashboard, `q` to quit |
| `nomctl support-bundle [--watch] [--since "12 hours ago"] [--output DIR] [--poll 10s] [--timeout 0]` | [Support bundle](/troubleshooting/support-bundle) |

## Alerts

| Command | Notes |
|---|---|
| `nomctl alerts setup [--code CODE] [--name NAME] [--relay URL] [--accept-privacy-notice]` | shows the privacy notice, pairs and starts the service |
| `nomctl alerts status` | pairing, service, per-alert state |
| `nomctl alerts list` | alerts with thresholds |
| `nomctl alerts enable ALERT` / `disable ALERT` | |
| `nomctl alerts set ALERT.KEY VALUE` | thresholds, e.g. `disk_low.min_free_gb 20`; also `pillar.name NAME` |
| `nomctl alerts test` | send a test message |
| `nomctl alerts unpair [--force]` | |
| `nomctl alerts run` | the daemon; used by `nomctl-alerts.service` |

## Other

| Command | Notes |
|---|---|
| `nomctl` | the [menu](/guide/menu) |
| `nomctl env` | every `NOMCTL_*` variable with its default |
| `nomctl completion bash\|zsh\|fish` | shell completion |
| `nomctl --version` | version, commit, build date, platform |
