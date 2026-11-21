# R13 node-engine contract and delivery plan

Status: proposed interface, not implemented. Companion to
[the chain-recovery design](2026-11-17-chain-recovery-design.md).

## Decisions from the second design pass

1. Ship the maintenance capability in znnd itself, using the selected release's
   own database and consensus code. Nomctl coordinates the operation. A second
   helper binary is a fallback, not the preferred distribution model.
2. Keep the original executable installed until the isolated candidate has
   passed offline validation with the replacement executable. The normal node
   stays stopped throughout. Promote the new executable and candidate data as
   one recoverable operation before replay. This still follows the operator's
   upgrade-then-rewind workflow, without exposing an unfinished database through
   the normal service.
3. An offline inspector must not call `node.NewNode`, `zenon.NewZenon`, or the
   ordinary app startup path. These initialize wallet/node state or open writable
   stores. Introduce a small maintenance context that explicitly acquires the
   instance lock, opens only the necessary databases, and returns typed errors.
4. Initially, treat an interrupted candidate as disposable. Rebuild it from the
   preserved stopped generation; do not resume an arbitrary partially applied
   reverse patch. An interruption-safe patch journal may be a later engine
   enhancement, but must not be inferred from the current `Pop` API.
5. Replay without loading the producer key. Enforce the mode in node startup and
   expose it in local status, not only in a menu flag. Persist the service
   invocation so restart and reboot retain the mode.
6. Fork recovery needs enforced peer and checkpoint constraints. Replacing
   `Net.Seeders` alone is insufficient: current startup enables discovery and
   seeds bootstrap discovery; it does not restrict connections to those peers.

## Two checkpoints, not one

The **rewind target** is the retained common ancestor, `(height, hash)`. The
**recovery checkpoint** is a later momentum on the intended branch. Require
`target.height < checkpoint.height` in fork mode. Reaching the target proves
the local rewind finished; reaching the later checkpoint proves replay selected
the requested branch. Neither an increasing height nor passing R4 proves that.

Nomctl can help discover the target by comparing the local node with explicitly
chosen reference nodes. Use `ledger.getMomentumsByHeight` in a bounded window,
record endpoint identities and observation times, and validate contiguous
height/previous-hash sequences. Show the highest matching ancestor followed by
the divergence. Do not binary-search unbounded history or treat inconsistent
responses as a valid chain. Capture the reference head/checkpoint and reject a
changed or internally inconsistent reference view.

An operator must select the trusted checkpoint/reference evidence; agreement
among arbitrary RPC endpoints is not a proof of canonicality. If the node RPC
is unavailable, defer local comparison until the stopped database inspector can
read the same window. If no common ancestor is found within supported depth,
offer restore-point/snapshot recovery instead of guessing a deeper rewind.

The UI should fill the exact target/hash from this comparison. Manual entry
remains possible for a published incident checkpoint, with the same validation.
Choosing "back 5" when the fork began 8 momentums ago must not be presented as
fork recovery. The operator can select 5, 10 or a custom count in replay mode;
fork mode must reach the verified common ancestor.

## Proposed znnd interface

These are interface proposals, not available commands:

```text
znnd maintenance capabilities --json
znnd maintenance inspect --data CANDIDATE --request REQUEST.json --json
znnd maintenance rewind --data CANDIDATE --request REQUEST.json --json
znnd maintenance verify --data CANDIDATE --request REQUEST.json --json
znnd --data DATA --recovery-policy POLICY.json
```

Capabilities must run without loading configuration, touching a data directory,
unlocking wallets, or starting profilers/network services. Emit one versioned
JSON result to stdout; ordinary startup banners must not precede it. Bounded,
redacted diagnostics go to stderr. Unsupported commands or protocol versions
cause nomctl to refuse the operation before downtime.

| Operation | Required contract |
| --- | --- |
| Capabilities | Protocol version, version/commit, database-format compatibility, supported network/genesis IDs, rewind limits, affected stores, no-production and peer/checkpoint enforcement support. Nomctl independently hashes the executable. |
| Inspect | Under the instance and database locks, open an existing stopped candidate without repair/create/compaction writes; validate genesis, frontier, exact target, and every required reverse patch. Report the validated maximum depth, affected accounts/stores, compatibility and an inspection digest. |
| Rewind | Bind the request to that candidate, executable, source frontier, target and inspection digest; revalidate under locks before any write; mutate only the isolated candidate through the engine. Reject a different frontier even if the supplied count still fits. |
| Verify | Close and reopen the candidate in a new process; confirm target height/hash, chain continuity, account/frontier/index consistency, and cache policy. Report completion only after data is durable. |
| Replay | Load a root-owned policy before any producer-key access; disable production, enforce the chosen peer/checkpoint constraints, and expose effective policy plus process/build/chain identity in local status. Missing, malformed or incompatible policy is a startup error. |

Inspection does not write chain, consensus or wallet contents; creating and
locking a dedicated runtime lock entry is permitted and reported separately.
It must not silently initialize, repair or migrate a database.

Requests and results use decimal strings for heights/counts so JSON consumers do
not truncate 64-bit values. Hashes are validated hex with explicit algorithms;
paths are absolute and bound to the candidate. No secrets, executable command
strings, or arbitrary shell snippets occur in the protocol. The helper requires
an existing candidate and must never silently create a new genesis database.

Every request binds at least protocol, plan ID, candidate generation ID,
network/genesis, expected stopped frontier, exact target, executable SHA-256,
policy digest, and inspection digest where applicable. Counts are derived from
the frozen heights after stop; unsigned underflow is an error.

Initial named error outcomes: `unsupported`, `busy`, `invalid_target`,
`changed_frontier`, `wrong_genesis`, `missing_reverse_patch`, `corrupt_patch`,
`incompatible_format`, `unsupported_boundary`, `insufficient_space`,
`candidate_incomplete`, and `verification_failed`. Nonzero exit plus a bounded
structured error is mandatory. A killed process or missing final result means
unknown completion, not success.

## Database scope and caches

Current code places chain history/state in `nom`, derived consensus information
in `consensus`, and peer discovery state in `network`. The node engine must
declare the complete affected-store set for its exact version; these directory
names are observations, not a timeless format contract.

The initial candidate includes independent copies of all declared chain and
consensus stores from one stopped generation. Do not share writable database
files through hardlinks. Wallet/config and network identity material remain
outside the rewind set. Peer-cache isolation is a separate explicit replay
policy, not permission to delete unrelated node state.

The preferred first cache policy is to rebuild derived consensus data **inside
the candidate**, preserving the original consensus directory. This remains a
proposal until tests establish it can reconstruct elections and period/epoch
statistics from chain state. If complete rebuilding cannot be proven, the
engine must implement and validate precise invalidation before advertising
rewind support. Do not assume the empty deletion callback is enough, and do
not prescribe deleting `consensus` on an operator's live node.

Inspection must verify reverse patches from the stopped frontier through the
requested target, including the oldest needed patch. On unsupported activation
or database-format boundaries, return a refusal with the limiting height.
Thirty momentums remains a provisional acceptance boundary to investigate,
not an assertion that all installed nodes can rewind thirty.

## Production and branch enforcement

Nomctl installs a persistent service override referencing a root-owned recovery
policy. The policy is read before producer-key lookup/unlock. Recovery mode must
skip producer-key loading and production workers while preserving the validation
needed for sync. This needs explicit tests: simply setting the producer pointer
to nil after ordinary startup is too late.

An ordinary restart keeps the override. A missing policy fails startup. Replacing
the executable with one lacking the recovery capability also fails startup.
Nomctl's general maintenance guard blocks unrelated upgrades/config changes
that would remove this constraint. Direct, intentional root bypass is outside
the contract; the normal nomctl/systemd paths must not bypass it accidentally.

For fork recovery, the node must enforce selected authenticated peer identities
for both outgoing and incoming connections, disable discovery and cached-peer
fallback during restricted replay, and reject data violating the pinned target
or checkpoint. Merely filling P2P `TrustedNodes` is not an allowlist; that
setting participates in admission exemptions in the current implementation.
Failure of selected peers means replay pauses, never silent fallback.

Production can be enabled only after a fresh same-process check confirms:

- expected checkpoint hash is present and the local head descends from it;
- progress beyond the original failure/fork and completed synchronization;
- usable peers under the selected recovery policy;
- expected installed/running executable and stable process identity;
- the operator explicitly confirms this plan's resume-production step.

Keep the producer disabled while stopping replay and preparing the normal start.
Record the confirmed transition durably before removing its production override;
recover any partial transition from observed files and actual service state.
Checkpoint and peer constraints must be separated from the production-disable
setting: resuming production must not silently discard branch constraints.
Changing back to ordinary discovery is a separately previewed policy change,
with the existing checkpoint enforced until explicitly retired.

## Implementation slices

| Slice | Repository | Completion evidence |
| --- | --- | --- |
| E1 — Offline inspection | go-zenon | No wallet/network startup, existing-DB locks, no writes or DB creation, exact target/history/format validation, versioned capability output. |
| E2 — Candidate rewind | go-zenon | 1/5/10/30-depth cases where supported; missing/corrupt patch refusals; independent reopen and logical-state equivalence after replay; cache/boundary proof. |
| E3 — Controlled replay | go-zenon | Producer key never loaded, no signing after restart/reboot, effective-mode status, inbound/outbound peer restrictions, persistent checkpoint enforcement. |
| N1 — Recovery transaction | nomctl | Verified update staging, protected snapshot, frozen target confirmation, disk guard, original/candidate ownership, journaled promotion and interrupted-operation status. |
| N2 — Operator controls | nomctl | Replay/fork wizard, bounded common-ancestor comparison, actionable progress/failures, explicit production resume, recovery that never starts a bad saved branch automatically. |

E1 is the next concrete coding task. E2/E3 must be proven before N1 can apply a
rollback to real data. N2's read-only preview can be developed against fixtures
earlier, but cannot imply that unsupported installed nodes can rewind.

## Acceptance matrix

| Case | Required observable result |
| --- | --- |
| Healthy chain, back 5/10 | Offline target matches; replay reaches the original frontier hash and advances; logical chain/account/index state matches an untouched control. |
| Fork 8 momentums deep, back 5 requested | Explain insufficient depth; no mutation. A valid common-ancestor target and later checkpoint are required. |
| Wrong branch with advancing head | Checkpoint mismatch is a recovery failure; producer remains disabled even if R4 says syncing/healthy. |
| Reference disagreement or movement | Refresh/resolve the reference evidence; never silently substitute a different target or checkpoint. |
| Missing oldest reverse patch | Inspect refuses before the first candidate mutation. |
| Node or helper already holds DB | Busy error; never break/remove another process's lock. |
| Power loss during rewind | Original generation unchanged; candidate marked unknown/incomplete and never started. |
| Power loss during data/binary promotion | Recover by journal and recorded identities; no mixed generation may start. |
| Replay restart/reboot | Policy still enforced before key loading; producer remains disabled. |
| Peer outage/unapproved inbound peer | Pause or reject; no discovery fallback and no production. |
| Epoch, period, spork or format boundary | Prove state/cache equivalence or refuse with an explicit supported-boundary reason. |
| Failure after enabling production | Report observed failure; never automatically repeat a rewind or restore a previous branch. |

## Additional source evidence

All references use official go-zenon commit
`e8986a63f164259053e0342ceaf9a68cb85d649e` inspected on 2026-11-17:

- [Node construction and startup](https://github.com/zenon-network/go-zenon/blob/e8986a63f164259053e0342ceaf9a68cb85d649e/node/node.go): wallet startup, discovery enabled, bootstrap nodes, instance `.lock`, and normal RPC startup.
- [Node configuration](https://github.com/zenon-network/go-zenon/blob/e8986a63f164259053e0342ceaf9a68cb85d649e/node/config.go): producer-key unlock and mapping seeders into P2P configuration.
- [LevelDB construction](https://github.com/zenon-network/go-zenon/blob/e8986a63f164259053e0342ceaf9a68cb85d649e/common/db/leveldb.go): the existing constructor does not request read-only opening.
- [Consensus storage](https://github.com/zenon-network/go-zenon/blob/e8986a63f164259053e0342ceaf9a68cb85d649e/consensus/storage/db.go): period/epoch points and election results have distinct persisted keys and in-memory caches.
- [Ledger RPC](https://github.com/zenon-network/go-zenon/blob/e8986a63f164259053e0342ceaf9a68cb85d649e/rpc/api/ledger.go): bounded momentum lookup for reference comparisons.
- [P2P admission](https://github.com/zenon-network/go-zenon/blob/e8986a63f164259053e0342ceaf9a68cb85d649e/p2p/server.go): discovery, static/trusted peers and connection admission are separate concerns.
