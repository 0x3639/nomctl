# R13 proposal: upgrade, rewind, and replay a stalled or forked node

Status: design and feasibility investigation; no rollback command is implemented.
User priorities: recover a stuck node by replaying recent momentums, and recover
from a fork by upgrading znnd and rewinding to before the fork.

The [node-engine contract and delivery plan](2026-11-17-rewind-engine-contract.md)
refines this proposal into concrete interfaces, branch/production controls, and
acceptance cases. The next coding task is E1: offline inspection in go-zenon.

## Operator workflow

The primary workflow is:

1. Notice that the node stopped producing; inspect diagnostics and chain evidence.
2. Choose and verify the updated go-zenon/znnd release containing the relevant
   fix, or an explicitly selected source revision. Show version, commit and
   executable SHA-256. Merely being the latest release is not proof of a fix.
3. Choose **replay recent momentums** or **recover from a fork**. Offer 5, 10,
   or a custom count, plus an exact target height/hash for fork recovery.
4. Preview the operation, recovery storage, target and expected downtime.
5. Enter one persistent maintenance window, stop production and the node,
   and preserve a verified, protected data/znnd pair plus an encrypted
   wallet/config backup. Bind the exact stopped frontier and target hash.
6. Use the verified replacement node engine to rewind an isolated copy offline
   while the normal service remains blocked. Keep the old executable in place
   until the candidate has passed offline verification.
7. Verify and reopen the resulting database at the requested height/hash,
   promote the checked copy and replacement executable through one recoverable
   transaction, and start in a mode that cannot produce momentums.
8. Replay/sync. Check branch identity, progress, peers and executable identity.
9. Separately confirm resuming production after the intended branch and sync
   are verified. Retain the original database and recovery point.

The upgrade must not briefly start a producer before the rewind. The existing
ordinary upgrade transaction starts the service after installation, so this
workflow must reuse its download/build/signature verification and staging
components, not invoke the whole normal upgrade command in sequence.

This rewinds one node's local state. It does not reverse momentums or transactions
on the network. Reconnecting normally may download the same momentums again;
that is expected for replay, but is not evidence that a fork was repaired.

## Two recovery modes

**Replay recent momentums:** choose N; the final target is stopped frontier H
minus N. Preserve the hashes being removed. When the canonical branch is
unchanged, the node is expected to reach the former frontier hash again and
advance beyond the original stall. Repeated failure at the same momentum is
reported with evidence and logs; there is no automatic deeper rollback loop.

**Fork recovery:** require a known-good common ancestor height AND hash, plus
a later checkpoint hash on the intended branch. A height alone is ambiguous
because competing branches can have the same height. Identify which evidence
or trusted reference nodes supplied the checkpoints. Record a reviewed peer
selection/reconnection plan; do not guess the canonical branch from one peer
or an unqualified majority of endpoints. If the common ancestor is unknown,
provide comparison evidence and stop before mutation. Being behind or not
producing is not by itself proof of a fork.

For both modes, the first preview may be made while the node runs. The final
target is frozen only after stopping. Show the exact from/to heights and hashes
for confirmation before database mutation. If a by-count target differs from
the live preview, show it explicitly; never silently change a requested exact
target. Counts must be positive, must not underflow, and must not remove genesis.

## What the current znnd source supports

Investigated official master at immutable commit
`e8986a63f164259053e0342ceaf9a68cb85d649e`, the merge of release v0.0.10,
retrieved from GitHub on 2026-11-17. An older local fork was not used as the
final capability baseline.

- [`chain/momentum_pool.go`](https://github.com/zenon-network/go-zenon/blob/e8986a63f164259053e0342ceaf9a68cb85d649e/chain/momentum_pool.go#L78)
  exposes an internal `RollbackTo` routine. It verifies the requested momentum
  hash, pops successive database revisions, and emits deletion events.
- [`common/db/versioned_db.go`](https://github.com/zenon-network/go-zenon/blob/e8986a63f164259053e0342ceaf9a68cb85d649e/common/db/versioned_db.go#L410)
  implements Pop using reverse patches. Applying the reverse state and deleting
  the forward/reverse patch records are separate calls. This is not evidence of
  an interruption-safe administrative transaction across the entire rewind.
  Missing rollback patches can return nil; preflight must check every required
  patch rather than assume any depth is available.
- [`protocol/chain_bridge.go`](https://github.com/zenon-network/go-zenon/blob/e8986a63f164259053e0342ceaf9a68cb85d649e/protocol/chain_bridge.go#L228)
  uses rollback for replacement side chains and rejects distances greater than
  30 momentums on that path. **30 is the network reorganization policy observed
  here, not proof of a storage limit or a safe administrative maximum.** The
  internal rollback method does not itself implement that limit.
- [`app/cli.go`](https://github.com/zenon-network/go-zenon/blob/e8986a63f164259053e0342ceaf9a68cb85d649e/app/cli.go#L54)
  registers version and license commands. The examined command flags and public
  ledger/stats API do not expose a maintenance rewind operation. Nomctl cannot
  safely implement this today by invoking a supported rollback command.
- [`chain/account_pool.go`](https://github.com/zenon-network/go-zenon/blob/e8986a63f164259053e0342ceaf9a68cb85d649e/chain/account_pool.go#L312)
  clears account-pool managers on momentum deletion. Bypassing the engine and
  removing raw database keys would bypass the coordinated state transition.
- [`consensus/points.go`](https://github.com/zenon-network/go-zenon/blob/e8986a63f164259053e0342ceaf9a68cb85d649e/consensus/points.go#L103)
  has an empty deletion listener, while readers elsewhere in that file compare
  cached end hashes and regenerate mismatched points. This requires explicit
  tests across period/epoch boundaries; it is not sufficient evidence that
  arbitrary offline rewinds leave every consensus cache correct.
- [`zenon/zenon.go`](https://github.com/zenon-network/go-zenon/blob/e8986a63f164259053e0342ceaf9a68cb85d649e/zenon/zenon.go#L75)
  normally starts the producer and protocol as part of node startup.
  [`pillar/manager.go`](https://github.com/zenon-network/go-zenon/blob/e8986a63f164259053e0342ceaf9a68cb85d649e/pillar/manager.go#L76)
  gates production on readiness, a configured key and its event window. The
  maintenance workflow still needs an explicit, persistent production-disable
  mechanism throughout replay; ordinary readiness is not branch verification.

Conclusion: a supported rewind feature appears feasible using the existing
engine, but needs work in **go-zenon as well as nomctl**. Five or ten momentums
are sensible initial acceptance cases, not a currently verified capability.

## Required node-engine interface

Prefer a maintenance CLI in the exact znnd release being used. A separately
shipped helper is acceptable only if its source revision, database format and
binary hashes are explicitly bound to that node build. Nomctl must not embed
an unrelated version of the engine or edit LevelDB records itself.

The interface needs:

- A capability/inspection result with protocol version, build identity,
  database format, network/genesis, frontier, target hash, available reverse
  patches, supported depth, and exact affected directories.
- A no-write planning operation and an offline rewind operation that acquire
  the node database lock, never open P2P/RPC listeners, and never load a signing
  key or start a producer.
- Validation of target existence/hash, positive bounded depth, genesis,
  contiguous reverse history, checksummed patch decoding and compatibility
  across activation or schema boundaries. Unsupported boundaries are refused.
- Consistent chain/account state, received-block and transaction indexes, and
  consensus/cache handling using engine APIs. Audit all persisted state before
  defining the complete affected-directory set.
- An explicit non-producing startup capability, durable across reboot and
  service restart, and a separately controlled exit from that mode. Wallet
  secrets should not be rewritten into ad-hoc temporary configuration.
- Structured completion evidence and failure outcomes; no secret output.

Initial depth policy: exercise 1, 5, 10 and 30 in isolated integration tests.
Offer custom counts only up to a depth the exact helper advertises and the
database can validate. Do not promise unlimited rewind. Deeper forks may need
a restore point before the fork, or a trusted snapshot/resync, until deeper
rewinds and their state/cache boundaries have been proven.

## Nomctl transaction and failure handling

Use R1 evidence, R3 recovery-file verification, and R4 health reports. Reuse the
principles of R11's durable operation state and start guards, but introduce a
general maintenance guard rather than pretending rollback is a migration.
Reject overlapping migration, restore, backup or upgrade operations.

Freeze a plan ID, network/genesis, source frontier, target height/hash, selected
release identity, original executable hash, candidate paths, recovery IDs,
producer state and peer/checkpoint plan. Do not persist wallet passwords.

Suggested states: planned → protected/stopped → candidate-ready → rewound →
offline-verified → promoted → replaying-with-production-disabled →
checkpoint-verified → production-enabled. Failed/interrupted states remain
explicit, with both file identities and the actual service/producer state.

Rewind a separately copied stopped database; never use hardlinks that let
candidate writes alter the original. Check capacity for the original, candidate,
restore point and migration of database format before downtime. Preserve all
related directories as one recoverable generation. Promotion must use a durable
intent record and recoverable same-filesystem swaps; do not assume renaming
several directories is one atomic operation. Leave wallet/config untouched.

On an interrupted rewind, discard or re-create only the isolated candidate
after confirming its identity. Do not retry a partly modified original. On
replay failure, keep production disabled, show the failed momentum/checkpoint,
and offer inspection, a new plan, or a confirmed restoration of the saved pair.
Restoring the old pair also remains stopped/non-producing: it might contain the
same faulty branch or vulnerable executable. No automatic downgrade/start.

Recovery is successful only after branch-specific evidence is satisfied. R4
can pass a progressing but still-syncing node; **that alone is insufficient to
resume production after fork recovery**. Require the expected checkpoint,
observed progress beyond the problem, synchronization and stable identity, then
an explicit production confirmation. Test producer/signing behavior across
restarts and previously signed slots before exposing this action.

## Proposed UI and CLI

Menu: **Backups & recovery → Recover a stalled or forked node**.

Flow: diagnosis → replay/fork choice → verified update → count or target →
recovery/space preview → stopped target confirmation → rewind and replay status
→ explicit resume-production confirmation.

Proposed names, not commands that work today:

```text
nomctl recover-chain plan --mode replay --momentums 10 --node-version TAG
nomctl recover-chain plan --mode fork --to-height H --to-hash HASH \
  --checkpoint-height C --checkpoint-hash HASH --node-version TAG
nomctl recover-chain prepare PLAN
nomctl recover-chain apply PLAN
nomctl recover-chain status [--json]
nomctl recover-chain resume-production PLAN
```

Prepare freezes the stopped frontier and recovery generation. Apply confirms
the finalized target and leaves production disabled during replay. A headless
invocation must supply explicit confirmation and plan identity. Restarting the
service or rebooting must not bypass the production gate.

## Implementation sequence and acceptance

1. Deliver E1 offline inspection, E2 candidate rewind, and E3 controlled replay
   in go-zenon as separate reviewable slices. E3 includes enforced peer and
   checkpoint policy as well as production disabled before key loading. Resolve
   cache and schema-boundary behavior in the engine.
2. Exercise copies of a deterministic devnet database: rewind N, reopen, replay,
   and compare logical chain/account/index state and hashes with an untouched
   control. Do not compare raw LevelDB file bytes as a correctness test.
3. Exercise an actual competing fork and peer reconnection plan; demonstrate
   reaching the intended checkpoint without producing on the abandoned branch.
4. Add the nomctl transaction, durable status, menu and confirmations on top of
   the proven engine interface.

Required failures: zero/excessive depth, genesis crossing, wrong/missing hash,
missing/corrupt reverse patch, incompatible build/genesis/schema, locked DB,
insufficient space, broken input DB, faulty/absent peers, same stalled momentum
after replay, wrong branch with advancing height, epoch/period/activation
boundary, cancellation or disk failure at every transition, crash/reboot during
promotion/replay, missing production-disable capability, and changed executable.

No live database was modified during this investigation. No go-zenon source
change or release is included in the current R4/R11 work.
