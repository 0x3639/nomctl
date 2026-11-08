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
a signed release. Both upgrade an existing node with a mandatory chain-data
backup and preserve its service configuration.

### Source upgrades

Choose **Install & upgrade › Upgrade a node from source**, select a repository
and branch, then confirm. From the command line:

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
3. Check the backup location and space, stop the service, and copy chain data.
4. Preserve the previous binary as `.previous`, then atomically install the build.
5. Start the service, finish the backup archive and hash, and apply retention.

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
4. Copy the existing chain data using the normal backup framework.
5. Atomically install the binary at `NOMCTL_INSTALL_DIR/NOMCTL_BINARY_NAME`
   (default `/usr/local/bin/znnd`), preserving the old binary as `.previous`.
6. Start the configured service and check that it is active.
7. Compress the pre-upgrade data, write its `.hash` sidecar, and apply normal
   backup retention.

The entire operation uses the same lock as backup/restore/deploy. The mandatory
backup bypasses scheduled cadence, uses `NOMCTL_BACKUP_DIR`, and includes `nom`,
`network`, `consensus` and `cache`. Wallet and `config.json` backups remain a
separate `nomctl backup wallet` operation. Existing service configuration is
preserved; the configured binary path must be the executable used by the service.

If the copy or installation fails, nomctl attempts to restart the original node
when it was previously running or starting. If the new service fails to start, nomctl finishes
the pre-upgrade archive and reports the retained previous binary for recovery.
It does not automatically run the old binary against potentially migrated data:
restore the matching pre-upgrade data before downgrading. An archive failure is
reported separately from a successful service start, and the uncompressed data
is retained under a unique `snapshot-*` directory in the backup directory. A
subsequent backup does not clear that recovery copy. An active service check
confirms systemd startup, not full chain synchronization.

During the backup and installation transaction, Ctrl+C and termination requests
are deferred until the upgrade or its recovery finishes. If a child command is
interrupted, its error follows the same recovery path and any incomplete snapshot
is removed. Wait for the final result before closing the terminal.

## Knowing when to upgrade

`nomctl status` and `top` check, at most every hour, whether a newer nomctl release exists and whether the configured go-zenon branch has commits beyond the running node's build. When either is true a line is added:

```text
Update    nomctl 0.5.0 available (running 0.4.0): sudo nomctl upgrade
Update    go-zenon master has new commits (deployed 1a2b3c4, remote 9f8e7d6): sudo nomctl upgrade-node
```

Nothing is printed when up to date or when the check fails. The result is cached in `/run/nomctl/update-check.json`. `NOMCTL_UPDATE_CHECK=false` (or `--no-update-check` on `status` and `top`) disables the check entirely for nodes that should not contact GitHub.

The same signal is available as the `update_available` alert, which is off by default:

```bash
sudo nomctl alerts enable update_available
```

It is informational: one Telegram message when an update appears, no reminders, and a "up to date" message after you upgrade or deploy.

## The relay

The relay is a container; upgrade it by redeploying the new image on Coolify. Pairings survive in the volume.
