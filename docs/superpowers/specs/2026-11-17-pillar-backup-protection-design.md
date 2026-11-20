# R14: pillar-aware backup and restore-point timing

Status: exact-input validation passed; the read-only resumable sync runner is
implemented experimentally. Full mainnet qualification and backup integration
remain pending. R14 is not included in v0.17.0-rc.1.

Constraint: calculate the schedule in a standalone runtime service using
existing RPCs. No new go-zenon RPC or engine modification is part of R14.

## Operator requirement

Delay a chain backup or creation of a recovery/restore point when its stopped-node
copy would overlap this pillar's upcoming momentum-production slots. Identify the
actual producer address and pillar name, show the upcoming schedule, and begin
only when the complete outage can fit between that producer's obligations.
Confirmed non-pillar nodes retain their current backup behavior.

Prioritize this work after the R11 two-server acceptance test, ahead of R13
rewind/replay and R5 metrics. It complements recovery verification and health
checks; it does not change who is entitled to produce a momentum.

## Verified engine behavior

Research is pinned to official go-zenon commit
[`e8986a63f164259053e0342ceaf9a68cb85d649e`](https://github.com/zenon-network/go-zenon/tree/e8986a63f164259053e0342ceaf9a68cb85d649e),
the examined v0.0.10 merge. Recheck capabilities against the installed build at
implementation time.

- The current [consensus constants](https://github.com/zenon-network/go-zenon/blob/e8986a63f164259053e0342ceaf9a68cb85d649e/vm/constants/consensus.go)
  specify 30 slots and 10 seconds per slot: a five-minute election round. These
  are production opportunities, not a guarantee that 30 momentums will appear.
  Missing momentums do not turn the future schedule into a height-based list.
- [Election generation](https://github.com/zenon-network/go-zenon/blob/e8986a63f164259053e0342ceaf9a68cb85d649e/consensus/election.go)
  supplies producer addresses, names, and slot start/end times. It chooses a
  historical proof momentum and uses that proof's delegation state. Cached
  elections are keyed by proof hash. The result must be treated as bound to the
  observed chain, not permanently fixed across forks.
- The [selection algorithm](https://github.com/zenon-network/go-zenon/blob/e8986a63f164259053e0342ceaf9a68cb85d649e/consensus/election_algorithm.go)
  is deterministic for its inputs. With fewer candidates than slots, addresses
  can appear repeatedly; an implementation must check every matching slot.
- The [consensus loop](https://github.com/zenon-network/go-zenon/blob/e8986a63f164259053e0342ceaf9a68cb85d649e/consensus/consensus.go)
  consumes these timestamped producer events. Reconstructing the schedule from
  recently observed producers or the current pillar ranking is insufficient.
- The examined [stats RPC](https://github.com/zenon-network/go-zenon/blob/e8986a63f164259053e0342ceaf9a68cb85d649e/rpc/api/stats.go),
  [ledger RPC](https://github.com/zenon-network/go-zenon/blob/e8986a63f164259053e0342ceaf9a68cb85d649e/rpc/api/ledger.go),
  and [pillar RPC](https://github.com/zenon-network/go-zenon/blob/e8986a63f164259053e0342ceaf9a68cb85d649e/rpc/api/embedded/pillar.go)
  do not expose the upcoming producer-event schedule directly. This does not
  prevent a standalone service from running the election calculation. Its
  correctness depends on obtaining the inputs for the relevant proof momentum,
  not merely on copying the deterministic algorithm.

## Reference calculator and accuracy limits

The [Zenon Observatory backend](https://github.com/0x3639/go-zenon-version/blob/7ade222f04da480167f704833607d5c74abebafc/backend/schedule.go)
already calculates a projection without changing go-zenon. It reads
`ledger.getFrontierMomentum`, 180 headers with `ledger.getMomentumsByHeight`,
and the current pillar registry/weights with `embedded.pillar.getAll`. It uses
go-zenon's pinned `consensus` library to select and order 30 producers per round,
using the last momentum strictly before the round's proof cutoff. Its own
standalone HTTP service publishes the list; that is not a new node RPC.

The reference pins `v0.0.9-alphanet`. The examined election, selection, context,
pillar-delegation ordering, and consensus-constant source files are byte-identical
to the corresponding files at the v0.0.10 commit above. This is a scoped source
comparison, not a claim that every engine release or all dependencies behave
identically. Pin and test the calculator against each supported engine build.

The [reference documentation](https://github.com/0x3639/go-zenon-version/blob/7ade222f04da480167f704833607d5c74abebafc/README.md#momentum-watch)
explicitly calls this an **RPC projection**. The engine computes elections from
historical state at the proof momentum. In contrast, `embedded.pillar.getAll`
combines the current registry with asynchronously cached weights, refreshed on a
five-minute cadence and not labeled with their election proof. Registration,
producer-address changes, ranking changes, cache refreshes, and forks therefore
need specific validation. One current weight list need not reproduce every
historical or upcoming election.

Initial investigation ran the reference's four focused schedule tests: its
fixture reproduced 150/150 observed producers, mixed history/wrong chain were
rejected, changed weights exposed mismatches, and failed refreshes retained the
original freshness timestamp. A live prediction saved at 2026-11-18 03:23:05 UTC
matched all 13 subsequently produced momentums through 03:26:10 UTC, spanning a
round boundary. Two public nodes agreed on the 213 retrieved headers. Six elapsed
slots were empty, leaving their assignments unverified. These are positive
samples, not certification of unobserved slots or transition cases.

Crucially, the reference's `ready` status does not require `matched == checked`.
Agreement counts cover produced momentums only; an empty slot cannot confirm
which pillar was assigned. Retrospective matches also do not by themselves
validate future predictions. Never treat HTTP success, `ready`, or a recent
perfect match count as permission to stop a pillar.

## Validated exact-input path

The [historical-state validation](2026-11-17-schedule-proof-validation.md)
establishes a working alternative to current-weight projection. A standalone
replica starts from pinned genesis and consumes the existing
`ledger.getDetailedMomentumsByHeight` RPC. The version-matched engine library
validates and replays the account blocks, embedded-contract execution, and
momentum state changes into its own database. It then reads the complete
election inputs at the selected proof hash and calculates the slots.

Controlled tests match every input and all 30 slots through registration,
revocation, producer rotation, delegation/ranking and balance changes, failed
contract calls, proof-cutoff boundaries, and equal-height fork replacement.
The suite also exercises 35 candidates, cache refresh, invalid/missing history,
and replica restart. A captured 60-momentum mainnet sample replays from pinned
genesis. Race detection and static checks pass. A deliberate current-RPC
counterexample disagrees on 10/30 and 30/30 slots across two rounds; the replica
matches both completely.

This selects **verified ledger replay** as the exact service's input basis.
Current pillar RPC data may help display identity, but cannot replace proof-state
inputs. The replica needs initial synchronization and additional disk space;
bounded early-mainnet resource measurements are now available, while full
mainnet bootstrap/catch-up and retention costs remain unqualified.
No second producer or new go-zenon endpoint is involved.

## Scope and identity

Apply protection to scheduled/manual chain backups and standalone restore-point
creation. File verification, archive compression, and cleanup run with the node
online and do not wait for a production gap.

Use the configured data directory and its Producer section to discover the
public producer address, then reconcile the service's executable, configuration
path, and start/configuration history. The existing RPCs do not establish the
loaded producer identity. Configuration changed after startup or insufficient
service evidence must produce an unknown result. Resolve the pillar name from
the address on-chain; the alerts display name, wallet owner address, or mere presence of a wallet file
must not identify the producer. Do not decrypt the wallet merely to query timing
and never display its password or private material.

Identity outcomes are explicit:

- **Non-pillar:** no active producer and no conflicting producer configuration;
  continue the existing backup path without schedule protection.
- **Pillar:** a configured producer address reconciled with the running service
  and its registered/scheduled pillar identity; enable protection automatically.
- **Unknown:** configured/running identity disagreement, unreadable configuration,
  unsupported identity evidence, unavailable RPC, or failed pillar lookup. Do not
  reinterpret uncertainty as a non-pillar node and stop it for an automated
  backup. Keep it online, explain the missing evidence, and defer.

An address absent from one election does not make its node a non-pillar. A
producer registration change can leave obligations in an already computed
round; use the slot's producer address and retain its proof-bound pillar name.
For older non-producing installations, the compatibility design must establish
that the running service loaded the inspected configuration before bypassing
the guard. Absence from a recent production sample is not evidence of either role.

## Standalone calculator and window service

Run a separate, unprivileged local service. It maintains its own verified state
using existing detailed-ledger RPCs and reuses a pinned consensus implementation,
preserving exact integer
weights, sorting, and Go pseudorandom/permutation behavior. It cannot stop/start
znnd or access wallet secrets. Nomctl supplies the reconciled public identity
and outage budget, retains the maintenance lock, and performs all service actions.
The initial runner publishes an expiring local status/schedule file. A private
client interface and window queries remain future integration; no public
listener or website availability is required.

Support continuous and on-demand operation with the same retained replica.
An on-demand start must catch up and revalidate before a backup can use the
schedule. Keep znnd online during warm-up and defer if catch-up cannot finish
within the backup's waiting policy. Continuous operation reduces timing
uncertainty; on-demand operation avoids idle work but needs a measured lead time.
The runner supports both lifecycles; backup-triggered startup is not yet wired.

The schedule response needs:

- Schema/calculator version, compatible engine builds, chain/genesis identity,
  generation/input times, RPC source, and sampled frontier height/hash/time.
- Each round's proof height/hash/cutoff and ordered timestamped slots with
  producer addresses and pillar names. Unknown future rounds stay unknown.
- Input provenance: `verified-ledger-replay` for the selected exact path, with
  bootstrap/verified-frontier evidence, input digests, coverage boundaries, and
  validation/mismatch reasons. Any separately displayed `rpc-current-weights`
  projection remains advisory and cannot authorize downtime.
- Per-round and total comparisons with observed producers, with explicit
  distinction between projected, observed, and unverified empty slots.

The window query accepts the producer address, full padded outage duration,
restart allowance, deadline, and nomctl's current node/configuration identity.
It returns `eligible`, `wait`, or `unknown`, the candidate interval, copy-abort
deadline, schedule/proof identity, recheck deadline, and an explanation. A
projection may be useful for display while the backup decision remains unknown.
Feed availability and backup eligibility are separate states.

Fetch a bounded, contiguous header history sufficient to establish every needed
proof cutoff; verify source/genesis, previous hashes, height continuity, and the
local node's agreement with the sampled chain. Retry an inconsistent sample and
invalidate candidates on changed proof hashes. Bound requests and computation;
an arbitrary distant date must not cause invented coverage. Reconcile service
clock and local readiness evidence in nomctl. Do not authorize maintenance from
an old UI preview or a cached external website response.

Before enabling automatic backups, synchronize the verified replica with the
local node and qualify each result against the same chain/proof and supported
engine rules. The experimental wrapper now provides bounded fetching, atomic
commits, status invalidation, and bounded fork discovery; current-mainnet
qualification, checkpoint/retention policy, and full resource measurements remain.
Bootstrap from genesis or resume the replica's
previously verified history. A supplied database snapshot is not proven merely
by a matching frontier hash. If history, input provenance, or chain agreement is
uncertain, return `unknown`. These are service delivery gates, not proposals for
an additional go-zenon endpoint.

## Window selection

Protect a time interval, not just a fixed number of future momentums. Display
the next X slots, but derive X from the operation's required outage and the
calculator's validated coverage.

The required window includes:

1. Graceful service stop and confirmation that its process has exited.
2. Copying all snapshot data and completing required local checks.
3. Restart, RPC readiness, peer reconnection, and catch-up to production readiness.
4. Explicit headroom for clock uncertainty, RPC age, and timing variance.

Compression stays after restart as it does today. Measure stopped-copy time
separately from total backup duration. Keep conservative recent measurements
with data size, source/destination filesystem, service, and znnd identity;
invalidate estimates after material changes. A first-run estimate needs a
bounded online copy/read calibration or an operator-supplied conservative
budget. Never learn the first estimate by stopping a live pillar blindly.

Accept a candidate only when its whole padded interval is covered by contiguous
validated slots and overlaps none assigned to the local producer. Treat slot
intervals consistently as half-open, then apply padding around their boundaries.
Recheck the current clock, service identity, producer, proof hashes, and schedule
immediately before stopping. Never start based on an earlier menu preview.

No RPC, stale frontier, unsynchronized clock, unsupported engine version,
unverified election inputs, replay mismatches, partial schedule, a fork/changed
proof, or a budget larger than the known gap means
**wait/defer**, not permission to proceed. If no gap can accommodate this data
size, explain that a faster copy or a supported snapshot mechanism is needed.
Copying a live database without a supported consistency mechanism is not a
fallback.

## Waiting and cancellation

Show pillar name/address, the next assigned slot, known schedule horizon,
estimated stopped interval, and why the request is waiting. Useful states are
`waiting-for-production-window`, `waiting-for-schedule`, `capturing`,
`restarting`, `completed`, `deferred`, `cancelled`, and `failed`.

Keep the node online while waiting. Do not hold the global operation lock for
an entire election round: that would prevent pausing the schedule or urgent
maintenance. Use a separate single-flight pending request per service; reacquire
the normal lock and revalidate everything before creating the snapshot. Only
one waiter may proceed. A migration guard always takes precedence.

Persist the request's schedule generation, operation, producer identity,
deadline, and reason without secrets. Pause, cancel, a changed schedule, or an
explicitly stopped node invalidates the waiting request; it must not resurrect
an obsolete backup or start a previously stopped service. Proposed maximum wait
and retry policy must be explicit in settings, with a bounded deadline and no
silent indefinite queue. If a delayed scheduled attempt expires, record it as
deferred and retain existing backups; do not label it successful or advance
successful-backup freshness.

This is an intentional new delay policy for production conflicts, distinct from
the existing rule that unrelated busy/missed daily jobs do not catch up. The
worker/restart policy must be specified and tested before enabling durable
resumption after reboot.

## Deadline and recovery behavior

Reserve an early copy-abort deadline before the next obligation, leaving the
full restart/readiness allowance. On timeout or cancellation during capture,
stop and reap the copy worker before restarting the original node; incomplete
copies never become complete restore points. Attempt restart before potentially
slow deletion of private partial data. Cleanup can happen after the producer is
ready. Preserve an actionable failure if restart fails.

An outage estimate and a process timeout cannot guarantee zero missed momentums
under arbitrary disk stalls, service failures, clock jumps, or a chain change.
The supported promise is: nomctl does not knowingly begin a protected backup
whose planned stopped interval intersects an assigned slot, and it abandons a
late copy early to restore production. Report actual readiness timing and any
missed obligation rather than claiming success from `systemctl start` alone.
Strict zero-downtime backup would require a separately supported filesystem or
engine snapshot mechanism.

Already-stopped nodes have no new backup-induced outage. Handle them explicitly
without starting the service or claiming active production protection.

## Shared restore-point callers

`backup.runSnapshot` owns stop/copy/start for ordinary backups and standalone
points. Node upgrades currently call `Snapshot.Capture` inside their own
stop/install/start transaction. R4 pre-restore snapshots and R11 protection
snapshots also reuse the backup machinery.

Audit every path before integrating the guard. An upgrade's copy-only budget
must not be presented as protection for its additional install/restart time.
Either supply and validate the entire composite outage budget or report that
the parent maintenance operation is outside this protection. Do not silently
bypass the guard in a low-level snapshot caller, split a transaction without
review, or weaken migration's persistent start blocks. Restoring old data,
rewinding/replaying, and the full stopped-node rsync transfer are separate
maintenance operations; R14 does not promise they fit between producer slots.

## Implementation sequence and acceptance

1. **Calculator validation — prototype passed:** the separate
   `experiments/scheduleproof` module reproduces historical inputs and complete
   controlled schedules from existing-RPC data. Keep this suite as the compatibility
   gate for engine upgrades. Complete full current-mainnet synchronization,
   resource measurements, and live comparison before deployment.
2. **Service and read-only nomctl preview — sync runner implemented:** durable
   replica lifecycle, bounded RPC catch-up/fork handling, and container packaging
   are tested. Complete live-tip qualification, then reconcile producer/pillar identity,
   validate schedule coverage and clock evidence, display projected slots and
   candidate windows with their qualification. Run in observation-only mode;
   do not stop znnd in this slice.
3. **Backup coordinator:** add measured budgets, cancellable waiting, lock
   revalidation, deadline-aware capture and restart/readiness checks; integrate
   manual and scheduled backups and standalone restore points.
4. **Other snapshot callers:** account for their complete transaction and show
   the exact protection scope; preserve all existing recovery guarantees.

Required regression cases include non-pillar behavior without an election
lookup; mismatched/missing producer evidence; repeated producer slots; no
assignments inside a fully covered window; an assignment just beyond the
visible list; round boundaries; unknown future coverage; clock drift/jumps;
stale RPC; a ready feed containing mismatches; unverified empty slots; changed
registration/producer/rankings; forked proof; data growth; slow stop/copy/start;
cancellation and partial-copy cleanup; pause/edit during waiting; two concurrent waiters;
operation-lock contention; migration guards; retained previous backups; and a
restart failure that does not get mislabeled as a protected success.

Before release, rehearse with a real pillar and a non-producing node. Record
the qualified calculator response, proof/input evidence, chosen interval,
stop/copy/readiness times, and observed production. Include a deliberately slow copy that is abandoned
before the next slot. If the measured restart budget cannot be met reliably,
leave automated protection deferred rather than relaxing the window check.
