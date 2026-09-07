---
title: Backups and restore
description: "Chain-data backups with checksums and retention, a systemd timer for scheduled backups, and restoring from an archive."
---

## Taking a backup

```bash
sudo nomctl backup [--max-backups N] [--cadence DAYS] [--hour HOUR] [--schedule]
```

A backup stops the node, copies `nom`, `network`, `consensus` and `cache` from the data directory into a staging area, restarts the node, and only then compresses the copy into `<backup dir>/go-zenon_backup_<MM-DD-YY_HHMMSS>.tar.gz` with a sha256 sidecar (`.hash`). The backup directory is `/backup` by default and `NOMCTL_BACKUP_DIR` changes it. Archives beyond `--max-backups` (default 7, maximum 30) are pruned, oldest first.

The backup directory must have at least 15 GB free (`NOMCTL_MIN_FREE_SPACE_KB`). If it does not, old archives are pruned first; if that is not enough the backup fails before touching the node.

Archives are written under a temporary name and renamed only after the checksum sidecar exists, so an interrupted run never leaves a complete-looking archive behind. Archives without a sidecar are ignored for retention and cadence.

If copying fails and the node was running before the backup, it is restarted before the error is reported; a node that was already stopped stays stopped.

## Scheduling

```bash
sudo nomctl backup --schedule --cadence 7 --max-backups 5 [--hour 3]
```

This writes `nomctl-backup.service` and `nomctl-backup.timer` under `/etc/systemd/system` and enables the timer. It fires daily at `--hour`, or when omitted at a minute and hour between 02:00 and 04:59 derived from the hostname, so many nodes do not back up at the same instant. On each run the cadence decides whether a backup actually happens: with `--cadence 7` a run is skipped when the newest archive is younger than seven days.

The service unit carries the backup, data and service settings and the free-space minimum in effect when it was created. Inspect the timer with:

```bash
systemctl list-timers nomctl-backup.timer
```

From the menu, a backup ends with an offer to set up scheduling interactively.

## Restoring

```bash
sudo nomctl restore --file go-zenon_backup_09-06-26_130405
sudo nomctl restore --file /mnt/usb/some-archive.tar.gz
sudo nomctl restore                                   # picker
```

`--file` accepts a path, or a bare archive name (with or without `.tar.gz`) inside the backup directory. Without it, on a terminal, a picker lists the 20 newest archives.

Restore verifies the archive against its sidecar, then inspects every entry: only directories and regular files inside `nom`, `network`, `consensus` and `cache` are accepted. An archive holding anything else, such as `config.json`, `wallet/`, links, devices or paths that escape the folders, is refused before the node is touched, so a restore can never overwrite files a backup did not make. The accepted folders are extracted into a private staging directory while the node keeps running; then the node is stopped, the current copies of those folders are moved into `<backup dir>/restore/<name>.bak.<timestamp>` as a safety snapshot, the staged folders are renamed into place and the node starts. Folders absent from the archive are left alone. If anything fails after the stop, the previous data is put back and the node restarted.

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

Restoring checks that the archive holds only wallet files and `config.json`, and, when its config names a producer key, that the key is present, opens with the archived password and matches the archived address. The current wallet directory and `config.json` are moved to `<backup dir>/restore/wallet-safety.<timestamp>/` before the archived files are put in place. The node reads them at start: pass `--restart` or run `sudo nomctl restart`.

The menu offers both under **Pillar**. The `wallet_backup_missing` alert (off by default) sends one message when a producer key exists without a backup newer than the key; see [Alert rules](/alerts/rules).
