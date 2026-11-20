# Read-only schedule synchronization runner

This standalone Go module implements R14's read-only synchronization slice.
It resumes a private verified replica, detects bounded forks, and publishes exact
current/next schedules only when its sampled source is current and agrees with
the replica. It does not modify go-zenon, access a producer's database, load a
producer key, or expose a new node RPC. Backup control is not enabled.

It pins `go-zenon v0.0.10-alphanet`, commit
`e8986a63f164259053e0342ceaf9a68cb85d649e`, independently of nomctl's dependency.
The module checksums are recorded in `go.sum`.

## What is being proven

`Mirror` starts from the pinned genesis in its own database. `ApplyJSON` consumes
items from the existing `ledger.getDetailedMomentumsByHeight` response. It
replays account blocks with the engine's momentum-height-specific rules,
regenerates embedded-contract results and descendants, and validates each
momentum and its state changes before committing. This follows the engine's
existing `protocol/chain_bridge.go` insertion order.

`ScheduleAt` selects the last momentum strictly before the election proof
cutoff, reads the replica's state at that hash, computes the active pillar
registry and delegation balances, and runs the pinned election algorithm.
It requires an observed momentum at or beyond the cutoff. Future proof cutoffs
that have not been covered remain unknown.

The tests compare the replica's entire election input set and all 30 slots
against a separate validator instance. The replica receives serialized public
ledger responses, never a copy of the source's state or election results.

Cases include producer rotation, registration/revocation, delegation/ranking
changes, undelegation, balance sends/receives, failed registration, exact proof
cutoff timing, equal-height fork replacement, 35 candidates and tied weights,
RPC cache refresh, missing/corrupt history, and reopening the private database.
Revocation uses a shortened lock interval in the controlled test network; it
is unchanged for mainnet replay. The 35-candidate network is a synthetic genesis.

A counterexample deliberately feeds the current pillar RPC's registry/weights
into the same election function: it differs in 10/30 old-round slots and 30/30
next-round slots following the fixture's producer/ranking changes, even with
synchronous cache refresh. The replica matches every slot in both rounds.

## Reproduce

Run from this directory with the host's `nodebox` helper. Dependency installation
and all Go code run inside the container. The image needs Go 1.24 or newer and a
C compiler for `-race`; the validation run used Go 1.26 on Linux arm64.

```sh
NODEBOX_IMAGE=golang:1.26-bookworm nodebox sh -c '
set -eu
export GOPATH=/home/app/go GOCACHE=/home/app/.cache/go-build
validation_tmp=$(mktemp -d /home/app/scheduleproof.XXXXXX)
export TMPDIR="$validation_tmp" GOTMPDIR="$validation_tmp"
trap '\''rm -rf "$validation_tmp"'\'' EXIT
go test -race -count=1 -v ./...
go vet ./...
'
```

After dependencies and the container image are cached, add `--offline` to
`nodebox`. The suite performs no network calls. Its mainnet fixture is included.
This nested module has a separate container CI job; a `go test ./...` run at the
nomctl root does not include it.
No production credentials are needed; signing keys used by the controlled
validator tests are the upstream public test keys or deterministically generated
fixture keys, never operator keys.

## Mainnet fixture provenance

`testdata/mainnet-2-61.json` contains 60 detailed momentums (heights 2–61) and
26 account blocks. It was read on 2026-11-18 UTC (November 17 in Chicago) using
`ledger.getDetailedMomentumsByHeight(2, 60)` from both
`wss://my.hc1node.com:35998` and `wss://node.zenonhub.io:35998`. Both sources
returned the same ordered momentum hashes. RPC enrichment such as confirmation
counts is not an election input.

The test replays this data from the engine's embedded mainnet genesis, including
signature, producer, transaction, and changes-hash checks. It reconstructs 26
pillar inputs at the selected proof and matches the 13 observed producers in
round 2 covered by the fixture. Empty and future slots in that real-chain sample
are not independently confirmed by this comparison; complete-slot comparisons
use the controlled validator.

- Fixture SHA-256: `f4a39c1f4d0202cbcf1dd5ae318515bed169adf641c32500cdca12cb2e8f6af3`
- Height 61 hash: `c0983e1331e0aa8932f3d9f409f3de993dcac47ef21aa088571128cb8042f662`

This small historical sample is not a full mainnet synchronization or a live
current-proof acceptance test. `SCHEDULEPROOF_MAINNET_FIXTURE` can override the
fixture path for additional captures with the same wrapper schema.

## Run a bounded sync

The image builds with CGO because the pinned engine's cryptography requires it.
The runtime is non-root, has no host credentials or producer files, and exposes
no listening port. Run from this directory:

```sh
export SCHEDULEPROOF_RPC=wss://my.hc1node.com:35998
docker compose build
docker compose run --rm --no-deps schedule \
  --rpc "$SCHEDULEPROOF_RPC" --data-dir /data \
  --duration 2m --max-blocks 5000 --batch 100 --poll 300ms \
  --report /data/measurement.json
```

Repeat the command to resume. The named volume keeps verified history between
runs. Use your local node's existing RPC for eventual local-node agreement;
the public endpoints in the measurement are research sources. No transaction
publication or administrative RPC method is available in the client.

For an ongoing service, use `docker compose up -d`; use `docker compose stop`
to stop it. Compose uses the same volume, a read-only root filesystem, dropped
capabilities, bounded memory/process counts, and no Docker socket or node-data
mount. Continuous syncing is explicit (`--duration 0 --max-blocks 0`). The
bounded validation work does not leave a background service running.

Read the status or the saved measurement:

```sh
docker compose run --rm --no-deps --entrypoint cat schedule /data/status.json
docker compose run --rm --no-deps --entrypoint cat schedule /data/measurement.json
```

A direct containerized CLI build is also available at `cmd/scheduleproof`.
`--rpc` and `--data-dir` are required; defaults bound a run to two minutes or
5,000 applied momentums. Requests contain at most 100 momentums and responses
are limited to 16 MiB. HTTP(S) and WS(S) are supported with request timeouts,
interruptible waits, and bounded retries. Genesis is downloaded once per run;
chain, frontier, and proof checks continue throughout synchronization.

## Continuous or on-demand operation

Both modes use the same durable database. An on-demand start resumes its last
verified momentum, catches up, and validates a fresh schedule before any future
backup coordinator may use it. Keep the watcher running through that decision;
a stopped watcher's old report is not a current authorization. The initial full
sync is required once, and longer offline periods require more catch-up work.
A short duration/block limit can end while still catching up.

Continuous operation keeps the state current for predictable backup timing.
On-demand operation avoids idle resource use but needs a warm-up allowance;
start before the scheduled backup, keep znnd online while catching up, and defer
if the watcher cannot become current. The present runner supports both process
lifecycles; automatic backup-triggered startup and window selection remain future
nomctl integration. Current-tip catch-up time has not yet been measured.

## Persistence and status contract

The data directory must be empty or have this runner's matching format, engine,
and genesis marker. Symlink paths, unrelated directories, concurrent writers,
and incompatible markers are refused. The database uses a separate atomic
adapter: each momentum's state, frontier, forward patch, and undo patch commit
in one synchronous LevelDB batch. This is not the engine's original on-disk
format and must not be supplied to znnd. No arbitrary snapshots are imported.

On restart the runner reads the committed database frontier, clears the old
schedule status, and reconciles its source before publishing again. Errors,
cancellation, incomplete batches, source changes, clock/freshness failures,
and unsupported proof coverage withhold schedules. An interrupted or rejected
replay can reopen the last committed state and retry; uncommitted account-pool
state is discarded. Process-kill tests exercise both sides of a commit.

A fork search is bounded to 120 momentums by default. A shared ancestor is
rechecked before rewinding only the private replica; replacement blocks are
then fully validated. A lagging source never triggers rollback. A fork beyond
`--max-reorg` remains unavailable and retains the existing replica. Following
a configured valid branch does not independently certify that it is the
operator's intended branch.

`status.json` is written atomically. Only `state: ready` carries schedules, and
only after exact equality with a fresh sampled source frontier. Results expire
at `validUntil` (at most 15 seconds, also capped by frontier freshness). Every
sync starts by clearing the prior ready result; orderly exit records `stopped`.
Consumers must honor both state and expiration and reject future generation
times after clock changes. The file is an experimental read-only feed, not a
backup authorization or an authenticated network API. No window planner or
stop/start control is included.

The measurement records elapsed and process CPU time, peak resident memory,
logical file sizes, RPC payload bytes/calls, committed heights, and schedule
comparisons made before applying later momentums. TLS framing and filesystem
allocation overhead are not included. Each applied momentum is also checked
by the engine's verifier. Mainnet resource results are in
[`measurements/2026-11-18`](measurements/2026-11-18/README.md).

## Remaining qualification

The bounded mainnet run is still far behind the current tip. Full synchronization,
current-tip live prediction checks, disk growth/retention, and modern high-activity
history still need qualification. Do not extrapolate whole-chain storage or
synchronization time from the early-mainnet sample. A supplied database plus a
matching frontier hash does not independently prove its whole state; resume
only this replica's verified history or begin at pinned genesis.

The module is single-owner serialized code. Its engine version and atomic store
format are fixed; upgrade/migration policy, disk capacity policy, a stable client
API, backup-window planning, and local service identity remain future R14 work.
