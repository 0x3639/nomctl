---
title: Upgrading
description: "Upgrade nomctl or a node from source or a signed release, with backups and recovery guidance."
---

## nomctl

```bash
sudo nomctl upgrade            # latest release
sudo nomctl upgrade --check    # only report
sudo nomctl upgrade --version v0.5.0
sudo nomctl upgrade --rollback # restore the previous binary
```

`upgrade` finds the latest GitHub release, downloads the archive for the host architecture together with `checksums.txt`, verifies the sha256, and replaces the running binary in one atomic rename. The old binary is kept as `nomctl.previous` next to it for `--rollback`. If `nomctl-alerts.service` is running it is restarted so the daemon uses the new code. Nothing else on the node is touched.

The installer one-liner does the same thing and remains a fine way to upgrade; `nomctl upgrade` just saves a trip to the README. `NOMCTL_REPO` (default `0x3639/nomctl`) selects the repository releases come from.

## The node

Use **Install & upgrade** in the interactive menu to choose a source build or
a signed release. **Before upgrading your node software, a restore point is
required.** nomctl creates it automatically during either upgrade; you do not
need to create it separately first.

The restore point backs up your current chain data and installed znnd so you can
recover if the upgrade causes problems. It records the znnd version and commit
when available, plus its SHA-256 checksum. Wallet and config use a separate
encrypted backup.

The restore-point question asks how to keep this required backup:

- **Indefinite** (the default): keep it until you select and delete it individually
  in **Manage restore points**. It is excluded from bulk pruning.
- **Choose days (1–30)**: after the chosen number of days, it becomes eligible for
  **manual pruning** in **Manage restore points**. It remains until you confirm
  deletion there.

**Neither option deletes restore points automatically.** The upgrade confirmation
shows your choice. `--yes` skips prompts, but still creates the required restore point.

### Source upgrades

Choose **Install & upgrade › Upgrade a node from source**, select a repository
and branch, choose the restore-point retention, then confirm. From the command line:

```bash
sudo nomctl upgrade-node
sudo nomctl upgrade-node --repo https://github.com/zenon-network/go-zenon.git --branch master
sudo nomctl upgrade-node --yes   # configured repository/branch, no prompts
```

Defaults come from `NOMCTL_REPO_URL` and `NOMCTL_BRANCH_NAME`; explicit `--repo`
and `--branch` override them. Source is the default: no mode flag is needed.
Use `--signed` only for a signed release; `--repo` and `--branch` cannot be
combined with it, and `--version` requires it. Non-interactive use requires
`--yes`. GnuPG and the private-release token are not needed for source upgrades.

The source-upgrade flow prints each step:

1. Check the existing node binary and service, then prepare build dependencies
   and the configured Go toolchain.
2. Clone the selected branch into a temporary checkout, record its exact commit,
   and build while the installed node keeps running. A clone/build failure
   aborts before stopping the node.
3. Check space for the protected restore point, preserve the current binary, stop the service, and copy chain data.
4. Preserve the previous binary as `.previous`, then atomically install the build.
5. Start the service, finish the restore-point archive and hash, and retain it until explicit UI deletion.

The node-operation lock covers the build and installation. Existing source
checkouts and systemd unit settings are preserved. Build staging is removed when
the operation ends. Recovery behavior and chain-backup exclusions are the same as
for signed releases below. Source builds use the repository and branch you select;
they do not undergo the signed-release verification described below.

For a new installation, use **Deploy a node from source** / `nomctl deploy`,
which also creates and configures the service. For an existing node's routine
source upgrade, use `nomctl upgrade-node`.

### Signed node releases

Run `sudo nomctl` and choose **Install & upgrade › Upgrade a node from signed release**,
or use the command directly:

```bash
sudo nomctl upgrade-node --signed                      # release picker; newest selected
sudo nomctl upgrade-node --signed --version v0.0.10     # exact tag, with confirmation
sudo nomctl upgrade-node --signed --yes                # newest published release, no prompts
sudo nomctl upgrade-node --signed --version v0.0.10 --yes
```

`--signed` explicitly selects the release download flow. Releases come from
[zenon-network/go-zenon-releases](https://github.com/zenon-network/go-zenon-releases),
ordered by publication date, newest first. Prereleases are included and labelled.
Drafts are excluded. An explicit tag can select an older version; review release
compatibility before downgrading. Non-interactive use requires `--yes`.

While the repository is private, supply a GitHub token with **Contents: read**
access to that repository through `NOMCTL_GITHUB_TOKEN`. For example, after
exporting the variable in your shell:

```bash
sudo --preserve-env=NOMCTL_GITHUB_TOKEN nomctl upgrade-node --signed
```

The token is redacted from configuration logs and is sent only to GitHub's API,
never to redirected asset hosts or the signing-key URL. Once the repository is
public, no token is needed. A 401, 403 or 404 response includes authentication and
rate-limit guidance.

Install GnuPG first if needed (`sudo apt-get install gnupg`). Linux amd64 and
arm64 are supported. The updater follows the repository's verification
instructions: import the key from `https://github.com/0x3639.gpg` into an isolated
keyring, verify `SHA256CHECKSUMS.txt.asc` against `SHA256CHECKSUMS.txt`, and require
this pinned primary-key fingerprint:

```text
310A 0EAE A844 9754 CF17 E8BB F6B8 2D11 5587 9DAB
```

It then checks the selected archive's SHA-256 against the signed checksum file.
Missing assets, a different signer, an invalid signature or a checksum mismatch
abort before stopping the node. There is no verification bypass.

Every step is printed to the terminal and the regular nomctl log:

1. Download the architecture's archive, checksum file, signature and signing key.
2. Verify the signature and archive checksum, then extract the binary.
3. Check the backup directory and free space, then stop the service and verify
   that it has reached a stopped state before copying data.
4. Create a protected restore point holding the existing chain data and matching installed binary.
5. Atomically install the binary at `NOMCTL_INSTALL_DIR/NOMCTL_BINARY_NAME`
   (default `/usr/local/bin/znnd`), preserving the old binary as `.previous`.
6. Start the configured service and check that it is active.
7. Compress the pre-upgrade data, write its `.hash` sidecar, and finish the protected restore point. It is never automatically pruned.

The entire operation uses the same lock as backup/restore/deploy. The mandatory
restore point bypasses scheduled cadence, uses `NOMCTL_BACKUP_DIR`, and includes `nom`,
`network`, `consensus` and `cache`. Wallet and `config.json` backups remain a
separate `nomctl backup wallet` operation. Existing service configuration is
preserved; the configured binary path must be the executable used by the service.

If the copy or installation fails, nomctl attempts to restart the original node
when it was previously running or starting. If the new service fails to start, nomctl finishes
the pre-upgrade archive and reports the retained previous binary for recovery.
It does not automatically run the old binary against potentially migrated data:
restore the matching pre-upgrade data before downgrading. An archive failure is
reported separately from a successful service start, and the uncompressed data
is retained under the restore point's `data/` directory. A subsequent backup does not clear that recovery copy. An active service check
confirms systemd startup, not full chain synchronization.

During the backup and installation transaction, Ctrl+C and termination requests
are deferred until the upgrade or its recovery finishes. If a child command is
interrupted, its error follows the same recovery path and any incomplete snapshot
is removed. Wait for the final result before closing the terminal.

Both source builds and signed releases use this same protected restore-point transaction. Before either upgrade, choose **Indefinite** (the default) or **1–30 days**. Days only mark eligibility for manual UI pruning; no restore point is deleted automatically. The CLI flag is `--restore-point-keep 7` or `--restore-point-keep indefinite`, with `NOMCTL_RESTORE_POINT_KEEP_DAYS=0` meaning indefinite. Space checks include the uncompressed copy, archive and saved binary before the service is stopped.

### Downgrading with matching data

For restore points created by v0.15.0 or later, choose **Backups & recovery → Restore from restore point**, or run `sudo nomctl restore point RESTORE_POINT_ID`. This restores the saved binary and its matching chain data together before starting the node. Subsequent upgrades cannot overwrite the binary stored in a restore point. See [restore-point management](/guide/backups#protected-restore-points) for retention and manual cleanup.

The following offline procedure is for **older standalone upgrade archives** that do not contain a protected matching binary.

Use the pre-upgrade archive reported by the upgrade and the binary that was
running when it was taken. A later upgrade can replace `.previous`, so confirm
that it still matches this archive. Keep the archive and its `.hash` sidecar.
If only an uncompressed `snapshot-*` recovery directory remains, keep it intact
and arrange recovery before proceeding with this archive-based procedure.

`nomctl restore` attempts to start the service after a successful restore and
after some failure-recovery paths. Block activation first so neither binary can
open the data until recovery is complete. These commands use the default service
and binary paths; substitute your configured values throughout. Stop on any
unexpected error and leave the guard installed.

1. Install a persistent systemd drop-in that prevents starts while the drop-in
   itself exists, then stop the service:

   ```bash
   sudo mkdir -p /etc/systemd/system/go-zenon.service.d
   sudo tee /etc/systemd/system/go-zenon.service.d/90-nomctl-offline-restore.conf >/dev/null <<'EOF'
   [Unit]
   ConditionPathExists=!/etc/systemd/system/go-zenon.service.d/90-nomctl-offline-restore.conf
   EOF
   sudo systemctl daemon-reload
   sudo nomctl stop
   sudo systemctl show go-zenon.service -p ActiveState -p Conditions
   ```

   Confirm `ActiveState=inactive` and that the new condition is listed before
   continuing. The guard also survives a reboot.

2. Restore the matching chain backup while the new binary remains blocked:

   ```bash
   sudo nomctl --skip-preflight restore --file /backup/YOUR_PRE_UPGRADE_ARCHIVE.tar.gz
   sudo systemctl show go-zenon.service -p ActiveState
   ```

   The restore must succeed and the service must remain inactive. Systemd skips
   its start attempt because the condition is false; a nomctl start-success
   message does not override that state check. If extraction, installation or
   rollback fails, leave the guard in place and inspect the reported recovery
   directories. Do not replace the binary or enable starts until the matching
   data has been restored successfully.

3. Install the matching previous binary while starts are still blocked:

   ```bash
   sudo install -m 0755 /usr/local/bin/znnd.previous /usr/local/bin/znnd
   ```

   If installation fails, keep the guard installed and the node stopped.

4. Once both the matching data and binary are in place, remove only this guard
   and start the node:

   ```bash
   sudo rm /etc/systemd/system/go-zenon.service.d/90-nomctl-offline-restore.conf
   sudo systemctl daemon-reload
   sudo nomctl start
   sudo nomctl status
   ```

Keep the backup and recovery copies until the node is healthy and synchronized.

## Knowing when to upgrade

`nomctl status` and `top` check, at most every hour, whether a newer nomctl release exists and whether the configured go-zenon branch has commits beyond the running node's build. When either is true a line is added:

```text
Update    nomctl 0.5.0 available (running 0.4.0): sudo nomctl upgrade
Update    go-zenon master has new commits (deployed 1a2b3c4, remote 9f8e7d6): sudo nomctl upgrade-node
```

Nothing is printed when up to date or when the check fails. The result is cached in `/run/nomctl-system/update-check.json` for root commands; the unprivileged alerts daemon keeps its own cache in `/run/nomctl/update-check.json`. `NOMCTL_UPDATE_CHECK=false` (or `--no-update-check` on `status` and `top`) disables the check entirely for nodes that should not contact GitHub.

The same signal is available as the `update_available` alert, which is off by default:

```bash
sudo nomctl alerts enable update_available
```

It is informational: one Telegram message when an update appears, no reminders, and a "up to date" message after you upgrade or deploy.

## The relay

The relay is a container; upgrade it by redeploying the new image on Coolify. Pairings survive in the volume.
