# R14: historical election input validation

Status: historical-input validation completed. The November 18 follow-up added
the read-only sync runner, atomic storage, restart/fork handling, and container
packaging; [bounded mainnet measurements](../../../experiments/scheduleproof/measurements/2026-11-18/README.md)
are recorded separately. Full current-mainnet synchronization remains pending.
No go-zenon endpoint or engine change is required by the validated approach.

## Decision

Use an independent, verified chain replica for exact schedule inputs. Bootstrap
from pinned genesis, consume the existing detailed-ledger RPC, and replay using
the version-matched engine's validation and VM code. Read the active pillar
registry, delegation mappings, and each backer's ZNN balance from the replica
at the election's proof hash. Then run the existing deterministic election
implementation. Keep all data separate from the producer's `.znn`.

The runnable prototype and fixture are in
[`experiments/scheduleproof`](../../../experiments/scheduleproof/README.md).
It pins official go-zenon `v0.0.10-alphanet` / commit
[`e8986a63f164259053e0342ceaf9a68cb85d649e`](https://github.com/zenon-network/go-zenon/tree/e8986a63f164259053e0342ceaf9a68cb85d649e).
The tests use a separate validator instance as the oracle. Only serialized
existing-RPC ledger data crosses into the replica; no historical state or
schedule is copied from the oracle.

Validation result: all 19 leaf cases passed with `go test -race -count=1`;
`go vet` also passed. These checks ran in an isolated Go container. The module
is independent of the normal nomctl test suite; its README records the separate
command needed to reproduce the results.

## Why the current-weight projection is insufficient

[`ComputePillarDelegations`](https://github.com/zenon-network/go-zenon/blob/e8986a63f164259053e0342ceaf9a68cb85d649e/chain/momentum/embedded.go)
reads the active registry and delegation mappings and sums delegators' confirmed
ZNN balances at the selected historical store. A delegation changes when its
contract receive executes successfully, not when its user send is submitted.
Balance movements also change weights without a delegation transaction.

[`GetPillarWeights`](https://github.com/zenon-network/go-zenon/blob/e8986a63f164259053e0342ceaf9a68cb85d649e/consensus/api.go)
reads a previous period's point. The public pillar response combines those
weights with the current registry, through an additional asynchronous cache.
Even a freshly refreshed response need not represent the next election's proof.

The controlled producer/ranking transition reproduced this failure: current-RPC
inputs caused 10 wrong slots out of 30 in the older round and 30 out of 30 in the
next round. The verified replica matched all slots in both. The separate cache
test confirmed that the reported weight remains old until the refresh and period
state advance. A perfect recent replay count cannot repair these missing inputs.

## Validation coverage

| Case | Result checked |
| --- | --- |
| Producer rotation | Old proof retains the old address; later proof selects the new address |
| Registration and revocation | Candidate membership follows successful historical contract execution |
| Delegation, ranking, and undelegation | Exact mapping and weights match the validator |
| ZNN send and receive | Confirmed balance movements reproduce historical weights |
| Failed registration | Attempt/refund is replayed; no nonexistent pillar enters the registry |
| Transition exactly at proof cutoff | Successful receive at cutoff is excluded; it enters a later election |
| 35 candidates | Top-30 boundary, tied weights, random selection, and a ranking change match |
| Fork replacing proof at the same height | Proof hash and inputs change; old coverage is invalidated after rewind |
| RPC cache refresh | Stale cached weights and later refreshed weights are observed explicitly |
| Missing or corrupted history | Missing heights/blocks, wrong chain, bad signature or state hash refuse service |
| Replica restart | Reopened verified history reproduces the same inputs and slots |
| Real mainnet heights 2–61 | 60 momentums and 26 account blocks replay from pinned genesis |

Each of the seven basic state-transition scenarios compares 90 slot assignments
(30 before, 30 after the change using the old proof, and 30 using the later proof),
along with the complete input sets. Additional cases check their own full rounds.
The real-chain fixture reconstructs 26 pillar inputs and matches 13 observed
round-2 producers; it cannot establish assignments for its empty slots. Controlled
validator comparisons cover every slot independently of observed production.

## Exactness boundary and service implications

Exact means the same schedule as the pinned validator for the same verified chain
and proof state. It does not certify that a valid fork is the operator's intended
branch, predict a proof not yet available, or guarantee restart performance.
The service must match the local node's frontier/proof and supported rules before
nomctl accepts a window. Changed proof, lag, uncertainty, or failed validation
means unknown/defer.

The existing
[`ledger.getDetailedMomentumsByHeight`](https://github.com/zenon-network/go-zenon/blob/e8986a63f164259053e0342ceaf9a68cb85d649e/rpc/api/ledger.go)
supplies the raw data needed by the tested replay path. The prototype follows
[`chain_bridge.go`](https://github.com/zenon-network/go-zenon/blob/e8986a63f164259053e0342ceaf9a68cb85d649e/protocol/chain_bridge.go):
apply account blocks with their containing momentum's rules, regenerate embedded
results/descendants, and validate/commit the momentum. Applying today's pool
admission rules to all historical blocks would be incorrect.

A bounded header cache plus current pillar weights cannot provide this exactness
in general. The selected design retains more state. Before production deployment,
measure a complete mainnet sync and incremental catch-up, storage/retention costs,
restart/crash behavior, and engine upgrades. A checkpoint may accelerate startup
only with an explicit trust/verification history; a frontier hash alone does not
prove a supplied database snapshot. Never open the producer's live database as
if it were the replica or introduce backup downtime merely to bootstrap it.

The prototype has no network listener, scheduler, producer key, or stop/start
control. Its successful tests establish the calculation/input route. They do not
claim that a production service has been deployed, that the present mainnet tip
has been fully replayed, or that automatic backups are enabled.
