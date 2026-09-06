---
title: Bootstrap
description: "Skip days of syncing: nomctl bootstrap downloads a verified chain snapshot and swaps it in, keeping wallet and config."
---

A fresh node syncs from genesis in days. `nomctl bootstrap` replaces the chain data with a published snapshot instead, so the node is in sync within minutes. It is the Go port of the hypercore restore-from-bootstrap script.

```bash
sudo nomctl bootstrap                      # the built-in snapshot URL
sudo nomctl bootstrap https://host/path/bootstrap-20260901.zip
sudo nomctl bootstrap --discard            # small disk: delete the old data instead of keeping it
```

The interactive menu has the same under **Restore Zenon from a bootstrap snapshot**. It asks for the URL, whether to keep the previous data, and confirms before starting. The command line does not confirm, like `resync` and `restore`.

## What happens

1. The `.hash` sidecar is fetched from the same URL with the extension replaced (`bootstrap-x.zip` → `bootstrap-x.hash`).
2. The archive is downloaded into `<backup dir>/bootstrap/`, with progress logged every few seconds. Free space in the backup directory must cover the archive plus `NOMCTL_MIN_FREE_SPACE_KB`.
3. The SHA-256 is verified. Nothing on the node is touched until it matches.
4. The archive is inspected: it must contain `backup/nom.bak/`, `backup/network.bak/` and `backup/consensus.bak/`, nothing else, with no path traversal or symlinks. Its extracted size must fit in the data directory with the same margin.
5. **Keep (default):** the snapshot is extracted into a staging directory while the node keeps running, then the service is stopped, the current `nom`, `network`, `consensus` and `cache` are moved to `<backup dir>/restore/<dir>.bak.<timestamp>`, the staged directories are renamed into place and the service starts.
   **`--discard`:** the service is stopped and the current directories deleted first, then the snapshot is extracted and installed. Use it when the disk cannot hold both copies.
6. The archive and sidecar are deleted. The wallet and `config.json` are never touched.

If a run fails after the download, run it again: a verified archive already in `<backup dir>/bootstrap/` is reused, not downloaded again.

Once the node has synced, delete the safety copy:

```bash
sudo rm -r /backup/restore/*.bak.*
```

## Snapshot source

`NOMCTL_BOOTSTRAP_URL` sets the default; the built-in value points at the snapshot published on DigitalOcean Spaces for this release. Snapshots are produced by a separate service; nomctl expects a `.zip` in the layout above and a sidecar holding the hex SHA-256 (a bare digest or a `sha256sum` line).

Disk needed, for the September 2026 snapshot: about 9 GB for the archive, 13 GB extracted, plus the old data if you keep it.
