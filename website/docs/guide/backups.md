---
title: Backups and restore
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

Restore verifies the archive against its sidecar, stops the node, moves the current `nom`, `network`, `consensus` and `cache` into `/backup/restore/<name>.bak.<timestamp>` as a safety snapshot, extracts the archive into the data directory and starts the node. If the safety move fails, restore aborts before touching any data.
