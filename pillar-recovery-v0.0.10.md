# Pillar fork recovery prerequisites for v0.0.10

**Safe fork recovery is not yet provided by this procedure. Do not use the
upgrade-and-resync sequence as a recovery runbook for a configured pillar.**

`nomctl upgrade-node` starts the installed node, and `nomctl resync` restarts a
previously running node while preserving its wallet and producer configuration.
An explicit `nomctl start` has the same production risk. The pillar can therefore
produce before an operator verifies its recovered branch, including between
the upgrade and the wipe. Stopping the service once does not prevent those
automatic restarts or a later boot from enabling production.

A safe fork-recovery procedure must wait for a verified way to keep production
disabled throughout recovery. The proposed R13 workflow is described in the
[chain recovery design](docs/superpowers/specs/2026-11-17-chain-recovery-design.md);
it is not an implemented operator command. The schedule-proof experiment does
not control production and does not supply this missing capability.

## Preparation that does not perform recovery

These commands assume the existing service is `go-zenon`, its data directory is
`/root/.znn`, and the binary is `/usr/local/bin/znnd`. Configure nomctl for the
existing paths if they differ.

```bash
sudo nomctl status
sudo nomctl backup wallet
```

Confirm that status identifies the existing node. Save the wallet-backup
passphrase securely and copy the encrypted archive off the server. These steps
protect wallet/configuration material; they do not establish a safe chain or
disable an already running producer.

Reference: [Wallet backup guide](https://nomctl.0x3639.com/guide/backups#wallet-and-config-backup).

## Required order for a future verified recovery procedure

1. Disable production before upgrading, wiping or restarting anything. Verify
   that the disabled state survives every automatic restart and reboot, while
   allowing the node to synchronize without producing.
2. Verify the exact v0.0.10 release and preserve recoverable chain data and
   binary material before changing them. Keep production disabled during the
   upgrade and empty-database resynchronization.
3. After synchronization, compare a momentum hash after the fork point against
   a known healthy v0.0.10 node at the same height. Matching heights or a
   `synced` status alone do not establish the correct branch. If verification
   fails or remains uncertain, leave production disabled.
4. Resume production only after branch verification and an explicit operator
   action. Confirm that the intended pillar identity is used and that no other
   instance is producing with it.

This checklist specifies acceptance criteria, not commands that currently
guarantee them. Do not proceed with fork recovery using this document until the
non-producing recovery path has been implemented and validated.
