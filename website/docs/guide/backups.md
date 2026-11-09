---
title: Backups and restore
description: "Chain-data backups with checksums and retention, daily cron scheduling with a 75% disk-use limit, and restoring from an archive."
---

nomctl distinguishes automatically managed **backups** from protected **restore points**:

| | Backups | Restore points |
| --- | --- | --- |
| Created by | Daily cron job, or the `nomctl backup` command | **Create restore point** in the menu, or either node-upgrade option |
| Contents | Chain data | Chain data and the installed znnd, with its version and commit hash when available, plus its SHA-256 checksum |
| Retention | 7 days by default, maximum 30; oldest backups may be pruned for space | Indefinite by default, or 1–30 days before eligible for **manual** pruning |
| Deletion | Automatic age/count/disk pruning, or explicit UI cleanup | **Only explicit deletion or confirmed pruning in the UI** |
| Restore menu | **Restore from backup**: data only; keeps installed znnd | **Restore from restore point**: choose data + saved znnd, or data only |

Wallet and configuration backups remain a separate encrypted operation.

In the menu, use **Schedule local backups** for daily protection and **Create restore point** for a manual save. Both **Restore from backup** and **Restore from restore point** remain available.

## Taking a backup

```bash
sudo nomctl backup [--retention-days DAYS] [--max-backups N] [--cadence DAYS]
```

A backup stops the node, copies `nom`, `network`, `consensus` and `cache` into a private staging directory, and restarts a previously running node **before compression**. The resulting `<backup dir>/go-zenon_backup_<MM-DD-YY_HHMMSS>_snapshot-<id>.tar.gz` has a SHA-256 sidecar (`.hash`). The backup directory is `/backup` by default; set `NOMCTL_BACKUP_DIR` to change it. Wallet files and `config.json` are excluded; back them up separately using the encrypted wallet backup below.

Retention defaults to **7 days**, with a supported range of **1–30 days**. Completed chain backups older than the chosen age are removed, oldest first. Manual backups also apply `--max-backups` (default 7, maximum 30). These settings apply to all ordinary chain archives, including those made by cron: after choosing 30-day scheduled retention, use `nomctl backup --retention-days 30 --max-backups 30` for manual runs if you want to preserve that window. Creating a restore point does not apply the ordinary age/count policy, though it may prune ordinary backups if needed for space. A manual `--cadence N` skips a backup when the newest archive is younger than N days; scheduled daily jobs always run without that skip.

### Disk-space protection

Backups keep total usage of the **filesystem containing the backup directory at or below 75%**, counting all files on that filesystem. Before stopping the node, nomctl estimates space for both the uncompressed copy and the compressed archive, assuming no compression savings. It also preserves `NOMCTL_MIN_FREE_SPACE_KB` (15 GiB by default) and a small safety margin.

When space is tight, completed chain backups for the selected service are removed oldest first, even if they are within the retention period. If the allocated space of all eligible archives would still be insufficient, disk-pressure cleanup retains them and the backup fails before stopping the node. Normal age/count retention still applies to ordinary backups. Disk capacity is checked again after stopping and before each data-write chunk during copying and compression. If space becomes insufficient mid-copy, the incomplete copy is removed and the previously running node is restarted. Compression failures remove the partial archive and retain the snapshot for recovery.

Wallet backups, restore safety copies, incomplete archives and recovery snapshots are never automatically pruned. They count toward disk usage and may need manual attention. Other processes can independently fill the disk between checks; this limit governs backup writes, not other applications.

Archives use a temporary filename and are published only after the checksum sidecar exists. Archives without a sidecar are ignored for retention and cadence. A node that was already stopped stays stopped.

## Schedule local backups

From **Backups & recovery**, choose **Schedule local backups**. The prompts ask how many days to keep backups (default 7, maximum 30) and which hour to run them (default 02:00 hour, in the server's local time). Setup does not take a backup immediately.

The equivalent command is:

```bash
sudo nomctl backup schedule --retention-days 7 --hour 2
```

This writes one root cron job to `/etc/cron.d/nomctl-backup` and enables the installed `cron` or `crond` service. Install your distribution's `cron` or `cronie` package first if neither is available. The job runs daily, using a minute derived from the hostname and service name to spread load between nodes. Without `--hour`, the hour is also derived, between 02:00 and 04:59.

The cron job preserves the backup directory, node data directory, service name, log path, retention and free-space minimum selected during setup. It keeps backups for the requested number of days, with an additional maximum of 30 archives; disk pressure can shorten retention. Rerunning setup replaces the existing job. An existing `nomctl-backup.timer` is disabled automatically, so both schedulers do not run together. Older timers keep working until you run the new setup.

Inspect or remove the schedule with:

```bash
sudo cat /etc/cron.d/nomctl-backup
sudo rm /etc/cron.d/nomctl-backup   # stop future scheduled backups
```

To take a backup now and then install the daily schedule, use:

```bash
sudo nomctl backup --schedule --cadence 0 --retention-days 7 --hour 2
```

Cron does not catch up on runs missed while the machine is off. An overlapping nomctl operation causes the backup to fail safely under the shared operation lock; the next attempt is the following day. Backup failures are recorded in the configured nomctl log (normally `/var/log/nomctl.log`), and the `backup_stale` alert recognizes both cron schedules and older timers.

## Protected restore points

Choose **Create restore point** under **Backups & recovery**, give it an optional name, and choose **Indefinite** or **1–30 days**. A restore point preserves chain data and the node binary currently installed at the configured binary path. It uses the same 75% disk-use guard and checks the combined copy, archive, and saved-binary requirement before stopping the node. The service restarts before compression.

The confirmation explicitly says it backs up **chain data and znnd**. nomctl records the saved znnd's version and commit hash when its `version` command or Go build metadata provides them, and always records its SHA-256 checksum. These details appear after creation, in the restore-point picker and management list, and before restoration. Missing version information, including in older restore points, is shown as **unavailable**; it does not prevent restoration. Version detection is bounded and happens before the service is stopped.

Both **Upgrade a node from source** and **Upgrade a node from signed release** create a mandatory restore point before replacing the binary. Both ask for its retention choice. For unattended use:

```bash
sudo nomctl backup point --keep indefinite --label "Before maintenance"
sudo nomctl upgrade-node --yes --restore-point-keep 7
sudo nomctl upgrade-node --signed --yes --restore-point-keep indefinite
```

Restore points live under `<backup dir>/restore-points/<id>/` with the chain archive, checksum, saved binary, and metadata. They are excluded from all routine backup pruning. **A chosen number of days is not an automatic deletion timer:** it only marks the point as eligible for manual pruning. Indefinite points never enter the bulk-pruning selection. If protected points occupy too much space, a new backup or upgrade fails safely and you can free space through the management menu.

### Restoring a restore point

Choose **Restore from restore point** and select a completed point. The UI shows its saved znnd version, commit hash and SHA-256 checksum, then asks whether to also revert znnd:

- **Yes — restore chain data and saved znnd** (recommended): verifies both checksums and restores the matching pair before restarting.
- **No — restore data only; keep installed znnd**: verifies and restores the chain archive without reading or replacing either node binary. The installed znnd must support the older data; incompatible versions may fail to start.

Review the selected behavior and confirm. Chain-data folders created after the point are moved aside too, so they are not mixed with an older state. Wallet and `config.json` remain unchanged.

The current chain data is retained under `<backup dir>/restore/`. When reverting znnd too, its current binary is kept in a unique `.nomctl-before-restore-*` file beside the installed binary. If binary installation fails, the previous chain data is put back before restart. If startup fails after a completed restore, the selected data/binary combination remains in place for inspection. The selected restore point is never consumed by restoration.

```bash
sudo nomctl restore-points                 # management UI; lists points when non-interactive
sudo nomctl restore point RESTORE_POINT_ID # asks about znnd in interactive use
sudo nomctl restore point RESTORE_POINT_ID --data-only # keep installed znnd
```

Non-interactive restoration defaults to the matching data/znnd pair. Pass `--data-only` explicitly to keep the installed znnd.

Incomplete points are shown in management for inspection or individual deletion, but cannot be restored or bulk-pruned. A failed finalization retains the uncompressed data in the point's `data/` directory for recovery.

### Freeing space in the UI

- **Manage restore points** lists each point's size and retention choice. Delete one selected point, or prune completed points whose chosen days have elapsed. The confirmation lists the exact points and approximate space to free. Indefinite points require individual selection; every deletion requires confirmation.
- **Manage backups** can delete one ordinary backup or preview backups older than a chosen age. It never includes restore points.

Neither menu expands its deletion list after confirmation. If another operation is running, the shared lock prevents concurrent cleanup. No command-line switch automatically deletes protected restore points.

## Restoring a backup

Choose **Restore from backup** under **Backups & recovery** to select an ordinary local archive. This restores chain data only; the installed binary is unchanged. Use a matching version when restoring older data across upgrades, or select a restore point to restore the matching pair.

```bash
sudo nomctl restore --file go-zenon_backup_09-06-26_130405
sudo nomctl restore --file /mnt/usb/some-archive.tar.gz
sudo nomctl restore                                   # picker
```

`--file` accepts a path, or a bare archive name (with or without `.tar.gz`) inside the backup directory. Without it, on a terminal, a picker lists the 20 newest archives.

Restore verifies the archive against its sidecar, then inspects every entry: only directories and regular files inside `nom`, `network`, `consensus` and `cache` are accepted. An archive holding anything else, such as `config.json`, `wallet/`, links, devices or paths that escape the folders, is refused before the node is touched, so a restore can never overwrite files a backup did not make. The accepted folders are extracted into a private staging directory while the node keeps running; then the node is stopped, the current copies of those folders are moved into `<backup dir>/restore/<name>.bak.<timestamp>` as a safety snapshot, the staged folders are renamed into place and the node starts. Folders absent from the archive are left alone. If moving or installing these folders fails, recovery puts back their previous state and attempts to restart the node. A failure to start after a completed chain-data installation is reported with the restored data still in place; inspect the service and retained safety copies before recovery.

## Wallet and config backup

Chain-data backups leave out the two files that let a node produce: the wallet directory, which holds the producer key, and `config.json`, which holds its password. They get their own small, encrypted backup:

```bash
sudo nomctl backup wallet                       # prompts for a passphrase, twice
NOMCTL_WALLET_PASSPHRASE=... sudo -E nomctl backup wallet   # scripts
sudo nomctl backup wallet --output /mnt/usb     # write somewhere else
sudo nomctl restore wallet FILE [--restart]     # prompts for the passphrase
```

The archive is a `tar.gz` of `wallet/*` and `config.json`, encrypted with [age](https://age-encryption.org) using a passphrase, written to `<backup dir>/wallet/<service>_wallet_<date>.tar.gz.age` with a `.sha256` sidecar, mode 0600. The chain-data retention rule never prunes it. Any machine with the `age` tool can open it without nomctl:

```bash
age -d -o wallet.tar.gz go-zenon_wallet_09-07-26_173701.tar.gz.age
tar -xzf wallet.tar.gz
```

`--no-encrypt` writes a plain archive; it contains the producer password, so only do that into storage that is encrypted already.

**Copy the archive off the node.** A backup on the node's own disk does not survive the node. Keep the passphrase with the copy: without it the backup cannot be opened, and there is no recovery.

Restoring checks that the archive holds only wallet files and `config.json`, and, when its config names a producer key, that the key is present, opens with the archived password and derives the archived address at `Producer.Index`.

Stop a running node first, or pass `--restart` to stop it for the restore and restart it afterward. An inactive node stays stopped. The service must be confirmed stopped before any live wallet or configuration files change.

The whole current wallet directory and `config.json` are moved to a unique private `<data dir>/.wallet-safety.<timestamp>-<random>/` directory before the archive is installed. Keeping this safety directory on the data filesystem allows atomic renames even when `NOMCTL_BACKUP_DIR` is on a separate disk. These safety files are **unencrypted** and contain the previous producer password; keep the directory private. After verifying the restored node and an independent encrypted backup, remove only the safety directory reported by that restore.

An installation failure puts back the previous files, removes paths that did not previously exist, and leaves the node stopped. If the restored node fails to restart, nomctl first confirms it is stopped, then restores the previous files and leaves it stopped for inspection. If that stop fails, the complete restored files stay installed and the previous files stay in the reported safety directory; stop the node before recovery. Any incomplete rollback is reported with the recovery directory. Inspect the data and safety directories before starting the node after an error.

The menu offers both under **Backups & recovery**. The `wallet_backup_missing` alert (off by default) sends one message when a producer key exists without a backup newer than the key; see [Alert rules](/alerts/rules).
