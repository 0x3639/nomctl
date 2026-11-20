# Pillar recovery: upgrade to v0.0.10 and resync

Run these commands **on the pillar’s existing server**, one step at a time. These instructions assume the standard installation:

- Service: `go-zenon`
- Data directory: `/root/.znn`
- Node binary: `/usr/local/bin/znnd`

If those differ, configure nomctl for the existing paths before proceeding.

## 1. Install nomctl

On Ubuntu/Debian, install nomctl and the signature-verification dependency:

```bash
sudo apt-get update
sudo apt-get install -y curl ca-certificates gnupg
curl -fsSL https://raw.githubusercontent.com/0x3639/nomctl/main/install.sh | sudo bash
nomctl --version
sudo nomctl status
```

Confirm that status finds the existing node.

Reference: [Installation guide](https://nomctl.0x3639.com/getting-started).

## 2. Back up the pillar’s wallet and configuration

```bash
sudo nomctl backup wallet
```

Choose a passphrase, save it securely, and copy the resulting encrypted archive off the server. This protects the existing producer key and configuration.

Reference: [Wallet backup guide](https://nomctl.0x3639.com/guide/backups#wallet-and-config-backup).

## 3. Upgrade the node to exactly v0.0.10

```bash
sudo nomctl upgrade-node --signed --version v0.0.10
```

Choose **Indefinite** for the restore point and confirm the upgrade. nomctl verifies the release, saves the existing chain data and binary, installs v0.0.10, and restarts the service.

**Wait for the upgrade and restore point to finish successfully before continuing.** If it reports an error, resolve that first.

Reference: [Upgrade guide](https://nomctl.0x3639.com/guide/upgrading#signed-node-releases).

## 4. Clear the forked chain data and start a full resync

**This deletes the local chain database and logs immediately, without a confirmation prompt. It preserves the wallet and `config.json`.**

```bash
sudo nomctl resync
```

After that succeeds:

```bash
sudo nomctl start
```

Resync automatically restarts a previously running node; `start` also covers a node that was stopped.

Reference: [Resync documentation](https://nomctl.0x3639.com/guide/service#resync).

## 5. Monitor progress and verify recovery

```bash
sudo nomctl status
sudo nomctl pillar status
sudo nomctl logs -f
```

Confirm the node reports **v0.0.10**, gains peers, and advances toward `synced`. Full synchronization from genesis can take days. Press **Ctrl+C** to exit the log viewer; the node keeps running.

Once synchronized, **compare a momentum hash after the fork point with a known healthy v0.0.10 node at the same height**. Matching heights alone do not establish that it is on the correct chain. The project’s recovery guidance confirms that a node on the old branch needs an empty-database resync after upgrading.

Reference: [Fork recovery guidance](https://forum.hypercore.one/t/go-zenon-v0-0-10-what-to-expect-at-the-community-spork-renewal/946).
