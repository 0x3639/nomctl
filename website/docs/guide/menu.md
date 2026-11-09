---
title: The interactive menu
description: "Grouped menus for managing nodes, backups, upgrades, Pillars and alerts."
---

```bash
sudo nomctl
```

The main menu has five groups and **Exit**. Each submenu shows its location
(such as **nomctl › Backups & recovery**) and a description of the highlighted
option. **Back** or **Esc** returns to the main menu. After an operation finishes,
fails, or is cancelled, you stay in the same submenu with that option selected.
Esc at the main menu exits; Ctrl+C cancels a prompt. Ctrl+C while following logs
stops the log view and returns to the submenu.

Opening the menu does not run hardware, NTP or Internet pre-flight checks. Those checks run when you choose node deployment, source upgrade, Pillar deployment or analytics setup. Service controls, configuration, diagnostics and recovery remain accessible during an outage. `--skip-preflight` also skips the setup checks.

## Manage node

| Entry | Runs |
|---|---|
| Live dashboard | [`nomctl top`](/troubleshooting/first-look) |
| View logs | `nomctl logs -f` |
| Start node | `nomctl start` |
| Restart node | `nomctl restart` |
| Stop node | `nomctl stop` |
| Node configuration | [Configuration](/guide/config): show, set a setting, or edit in `$EDITOR`; offers a restart |

## Backups & recovery

Use **Schedule local backups** for automatic daily backups and **Create restore point** to save a protected recovery point manually. Both backup and restore-point restoration are available below.

| Entry | Runs |
|---|---|
| Restore from backup | Choose and restore a local chain-data archive |
| Back up wallet & configuration | Encrypted wallet and `config.json` backup |
| Restore wallet & configuration | Choose a wallet backup, enter its passphrase and restore |
| Create restore point | Save chain data and matching node binary; choose days or indefinite |
| Restore from restore point | Verify and restore the matching chain-data/binary pair |
| Manage restore points | Show sizes; delete one point or preview manual pruning of eligible points; confirmation required |
| Manage backups | Delete a selected ordinary backup or preview old backups to prune |
| Schedule local backups | Daily cron backups; asks for retention (1–30 days, default 7) and hour, with automatic pruning at the 75% disk limit; does not run a backup immediately |
| Restore from "trusted" bootstrap snapshot | [Bootstrap](/guide/bootstrap): URL prompt, keep/discard choice and confirmation |
| Resync from scratch | Wipe chain data and resync, after confirmation |

For bootstrap, "trusted" means a snapshot publisher you choose to trust. The
hash sidecar verifies download integrity; it does not authenticate the publisher.

## Install & upgrade

| Entry | Runs |
|---|---|
| Deploy a node from source | [Deploy](/guide/deploy), with repository and branch pickers |
| Deploy a Pillar | [Pillar deployment](/guide/pillar), using official go-zenon master and producer-key setup |
| Upgrade a node from source | [Source upgrade](/guide/upgrading#source-upgrades) (`nomctl upgrade-node`): pick a repository/branch, build before downtime, back up and install |
| Upgrade a node from signed release | [Signed upgrade](/guide/upgrading#signed-node-releases) (`nomctl upgrade-node --signed`): choose a release, verify its signature/checksum, back up and install |
| Upgrade nomctl | Confirm and run the latest-release self-upgrade; reopen nomctl afterward to use the new version |

The signed-release picker selects the most recently published release by default.

## Pillar & orchestrator

| Entry | Runs |
|---|---|
| Pillar status | Show producer address, key file and index |
| Set up producer key | Create or configure a [producer key](/guide/pillar) |
| Orchestrator status | Show the configured orchestrator service state |
| View orchestrator logs | Follow orchestrator logs |
| Reset orchestrator | [Hard reset](/guide/orchestrator), after confirmation |

Wallet backups are under **Backups & recovery**; Pillar deployment is under
**Install & upgrade**. Orchestrator operations require an installed service.

## Alerts & tools

| Entry | Runs |
|---|---|
| Telegram alerts | [Alerts](/alerts/overview) setup, or status when already paired |
| Set up Grafana dashboard | [Analytics stack](/guide/analytics) installation |
| Create support bundle | [Support bundle](/troubleshooting/support-bundle) |

The menu needs a terminal. Over a plain pipe nomctl points at the CLI commands.
Every operation is also available directly for automation; see [Commands](/reference/commands).
