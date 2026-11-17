# R4 and R11: observed recovery and controlled device migration

## R4

Before upgrades and chain restores, record service/RPC/znnd identity. After
installation and recovery-point finalization, observe a bounded window of RPC,
heights, frontier age, peers, process identity, and restart counts. Report
healthy, syncing with observed progress, stalled, failed, or unknown. Missing
evidence is never success. Require the running executable to match the intended
SHA-256; report RPC version/commit without inferring either from a label.
Compare pre/post observations, but only measure progress within the same
post-change service incarnation. A restart during the observation window fails
the check. A healthy still-syncing node may pass; catching up is not required.

Restore preserves a protected pre-restore data/binary pair in a separate backup
stop/start cycle before beginning the requested restore. Failures report its
ID and offer a separately confirmed recovery in interactive use, after releasing
the original operation lock. Noninteractive failures print the recovery command
and return nonzero. Never automatically downgrade a binary or enter a rollback
loop. An explicit data-only restore remains data-only in guided recovery.

## R11

The initial migration supports systemd Linux hosts with the same architecture,
root-operated znnd, and the migration-capable nomctl installed on both hosts.
SSH must already authenticate noninteractively with a trusted host key. No host
key is accepted automatically. Rsync and SSH run with argument arrays, strict
host checking, and quoted remote commands. No wallet/config values or filenames
are streamed into command logs.

Preview checks host identity, architecture, installed executable, configured
data paths, source service, destination absence, disk capacity, rsync, RPC/P2P
settings, and producer configuration. Before any handoff, create and verify a
protected restore point and a separate encrypted wallet/config backup. Transfer
the exact installed znnd and generate the destination service without starting
it. Require the destination data directory and conflicting installation to be
absent. Do not overwrite an existing node.

An SSH control session holds the remote operation lock across preparation,
rsync, verification, promotion, and start. Both hosts persist a migration guard
and systemd start condition before stopping. Normal nomctl operations refuse a
guarded host, including scheduled mutations after a disconnected session or
reboot. Durable state binds the migration ID, both host identities, paths,
executable digest, and phases. Resume requires the same binding. The source
stays guarded after successful migration; source files are never deleted.

Copy the entire stopped data directory with rsync, including dotfiles, wallet,
configuration, file modes, timestamps, hardlinks, and internal relative links.
Reject special files and all external, dangling, or absolute symlinks before
downtime and again before promotion. Use a private destination staging directory
and resumable partial files outside the data tree. Verify contents and required
metadata against the stopped source before atomic promotion. The source and
destination remain stopped on copy/verification failure. No rsync deletion may
target live data.

The operator separately confirms producer handoff before starting the
destination. Recheck the source's persistent start block and stopped state,
then start the destination and apply R4 plus producer/config validation. Failures
do not restart the source. Returning to the source first stops and durably blocks
the destination (including at boot), then unblocks the source within the separately confirmed return command. Lost connectivity at any boundary leaves a recoverable,
explicit state; it never guesses which producer is safe to start.

The menu and CLI support preview, transfer/resume, handoff, status, and return.
Migration does not move external backups, schedules, alert configuration, or
orchestrator data. The completion report lists those follow-up setup actions.
Validation uses deterministic service/RPC fixtures and two isolated endpoints
with real rsync, covering interruption, space exhaustion, invalid links,
verification failure, conflicting destination, failed startup, and return/boot
guard behavior. Deployment on real operator servers requires their addresses;
no live migration is performed as part of implementation.

## Acceptance evidence

Tests use two isolated directory endpoints and controlled service/RPC fixtures.
Real rsync also exercises the production receiver and JSON control protocol
through a local SSH-shaped pipe. This does not establish real SSH authentication,
real systemd reboot behavior, or node health on an operator server. Those live
acceptance checks remain required before release. Migration state is retained
for review after complete/returned phases; another migration requires a reviewed
reset outside this initial workflow. Extended attributes and ACLs are outside
the preserved metadata set in this version.
