# Bend contracts for nomctl

The root `LAWS.bend` contains 73 laws, with proofs in `PROOF.bend` and an
executable specification in `test/bend/model.bend`. The coverage inventory maps
39 areas across the entire maintained repository to laws and executable checks:
all Go packages, the separate schedule-proof experiment, website, deployment,
installer, documentation and verification tooling.

**This is subsystem contract coverage, not a proof of the entire Go program.**
Bend proves the stated properties of the Bend model. Direct comparisons check
selected model decisions against real Go functions; existing integration and
regression tests check the broader subsystem contracts. The inventory ensures
that an area cannot be silently omitted. It does not establish line or branch
coverage, prove every behavior, or make every sentence in a contract a theorem.

## Run the checks

Docker must be running. Local checkouts must be under `/Users/user` for Colima.
Use the installed `nodebox` helper; there is no host Node/Bend fallback.
Host Python uses only its standard library. Container images contain Go, Bend,
Lean and the migration test tools; they are development dependencies, not part
of either shipped binary.

```bash
make law-images
(cd website && nodebox npm ci)
export PATH="$PWD/scripts:$PATH"
bend guide
bend PROOF.bend
make laws
```

`make proof` runs the regular proof checker. `make laws-inventory` checks the
coverage map without running dependency code. Neither substitutes for `make laws`.
Rebuild `make law-images` after changing Go dependencies or toolchain pins.
The full gate fails if a required image, nodebox, test, proof or build is missing.
Validation builds disable Go's Git version stamping so container checkout
ownership and shallow PR metadata cannot prevent compilation.

Checks run without network access; dependency downloads happen only during
image builds and `nodebox npm ci`. Bend and Go run as non-root users with read-only
source mounts, dropped capabilities and bounded resources. Installer fixtures
alone run as root inside their isolated, read-only container because the actual
installer requires root. They replace downloads with synthetic local archives
and install only into a temporary directory. No live node, wallet, service,
Telegram chat or production directory is used.

Bend 2.0.40 release archives are checked against the two SHA-256 values from the
[official installer](https://bend-lang.com/install.sh). Its base image is pinned
by digest. The independent checker pins the elan installer commit and checksum
and Lean 4.34.0. Elan/Lean binary downloads are version-selected, not independently
checksummed here. Go and Node images follow their declared version tags. These
are explicit toolchain trust boundaries, not a fully hermetic supply chain.

## What the full gate does

1. Checks that every maintained file belongs to exactly one contract area, all
   referenced laws/proofs exist, and all named test functions still exist.
2. Runs `bend PROOF.bend` and `bend PROOF.bend --verdict`, the latter using
   Bend's separate Lean-based kernel. Neither unsafe nor unfinished proofs pass.
3. Evaluates fresh expected results with Bend and compares them with actual Go
   implementations. No Python or Go reimplementation supplies the oracle.
4. Deliberately breaks seven copies of the model: lock ownership, verification,
   schedule generations, migration exclusion, retention, redaction and alert
   size limits. Each must fail as a proof mismatch. Live sources are untouched. Four additional Go overlays deliberately break
   service-state classification, retention, alert-name validation and wallet
   preservation; the matching comparison test must detect each one.
5. Runs both Go modules with the race detector, including rsync migration
   rehearsals. Every specifically named evidence test must actually pass;
   a skip, missing result or an empty test run fails the gate.
6. Runs the website plugin tests and production build, installer fixtures,
   inventory failure tests, Go vet and builds of both modules.

Independent checks run concurrently. Parallelism does not relax the ordering
of service stops, operation locks, backup publication or migration activation.
Bend's small decision predicates have no balanced computational workload worth
forking; adding parallel syntax to them would only add overhead.

Each invocation writes fresh logs and generated comparisons under the ignored
`.bend-work/run-*` directory. Failed checks retain their full output there.
`BEND_IMAGE`, `BEND_VERDICT_IMAGE`, and `LAW_CHECK_IMAGE` can select explicitly
built alternatives for toolchain investigation; default runs use the pinned
repository images. Do not claim the default verification result after overriding
those images without recording the alternatives.

## Direct implementation comparisons

| Family | Cases | Real behavior exercised |
|---|---:|---|
| Retention | 1,254 | `backup.SelectForPruning`: counts 0–32, keep -2–35, oldest selection, caller list unchanged |
| Service states | 10 | `service.State.Stopped`: all declared states |
| Data scope | 16 | `backup.Folders` and actual `resync.Wipe` on eight synthetic directories |
| Health outcomes | 7 | `health.Report.Passed`, including empty and unknown strings |
| Alert validation | 384 | `alertproto.ValidAlert`: known/event/unknown names, state/severity combinations and six text-length boundaries |
| Configuration | 24 | `nodeconfig.Effective`: absent/present settings, values and defaults |

These 1,695 comparisons are bounded executable evidence. The alert adapter
classifies actual names, states and lengths into the model's categories; it does
not prove every alert rule or all possible strings. Naturals in the retention
model are unbounded; tested Go values are small integers. Negative retention
counts are normalized as required by the existing Go behavior. The configuration
model abstracts a scalar setting; schema validation and JSON preservation have
separate existing tests.

The schedule and migration laws include induction over arbitrary finite model
event histories. They prove the abstract transition invariant, while the Go
schedule/migration tests exercise concrete filesystem, lock and service flows.
There is no generated Go implementation or unbounded refinement proof connecting
those histories to every Go execution. Redaction laws assume a value has already
been classified as secret; concrete classifier behavior is covered by tests.

## Subsystem contracts

`test/bend/coverage.json` is the authoritative, machine-checked map. The table
below is a readable index; review the named laws and tests when changing an area.

| Area | Required behavior |
|---|---|
| `alertproto` | Reject unknown, oversized or incorrectly classified alerts; authenticate identity, body and timestamp. |
| `alerts` | Retain failed deliveries for retry; disable reminders for informational events; run unprivileged. |
| `analytics` | Validate monitoring configuration before activation, preserve unrelated jobs, bind generated services to loopback. |
| `backup` | Back up only chain folders, exclude incomplete archives, preserve protected points, enforce retention and current schedule generations. |
| `bootstrap` | Verify downloads and archive scope before downtime; recover previous data on install failure. |
| `config` | Use validated defaults and environment overrides; redact credentials. |
| `deploy` | Complete a source build before stopping the service; quote configured paths. |
| `doctor` | Bind evidence to the observed process and preserve uncertainty and partial results without leaking logs. |
| `execx` | Bound command output and duration; preserve failures and interrupt semantics. |
| `fsx` | Publish runtime files atomically in private writer directories; reject unsafe filesystem objects. |
| `health` | Success requires stable process identity and chain progress; unknown, failed, stalled and canceled checks cannot pass. |
| `lock` | Allow one mutating owner; preserve migration guards and release on errors. |
| `logx` | Sanitize untrusted console text and retain requested logging destinations. |
| `metrics` | Report only current snapshot identity; do not mistake busy ledgers for unreachable nodes; bound sample history. |
| `migrate` | Never activate both nodes during handoff; keep durable source blocks across failure and restart; validate transfer scope. |
| `node` | Preserve RPC errors and partial snapshots; bound request durations. |
| `nodeconfig` | Validate edits before atomic save; preserve unrelated configuration and resolve defaults only when absent. |
| `nodeupgrade` | Verify release identity and checksums, build before downtime, confirm stop and keep matching recovery material. |
| `orchestrator` | Refuse unsafe hard-reset paths and unavailable services before mutation. |
| `preflight` | Check resource and time synchronization prerequisites before deployment. |
| `producer` | Validate configuration before creating keys; preserve key/address compatibility and explicit replacement intent. |
| `recovery` | Verification never changes live data; results bind all selected artifact components; schedules reject stale generations. |
| `relay` | Authenticate before consuming quota; isolate nodes and chats; retry failed delivery; independent nodes can proceed concurrently. |
| `restore` | Validate hostile archives before stopping services; restore a matching pair or preserve rollback material. |
| `resync` | Delete only the documented chain folders after confirmed stop; preserve wallet and configuration. |
| `service` | Unknown and transitional states never count as stopped; stop waits for a confirmed terminal state. |
| `support` | Redact credentials and peers, bound collection and publish only to fresh private destinations. |
| `tui` | Revalidate destructive selections, hold operation locks and preserve errors/recovery context. |
| `ui` | Execute each requested action once and return its failure to the caller. |
| `update` | Verify fetched releases and atomically replace with rollback; never reuse another writer temporary file. |
| `walletbackup` | Encrypt wallet material; verify identity before replacement; retain original existence and recovery files on failure. |
| `commands` | Flag precedence, root authorization, diagnostic access and lock boundaries remain consistent between CLI and menu. |
| `relay-entrypoint` | Relay entrypoint builds with its authenticated and isolated server. |
| `dashboards` | Embedded dashboards remain valid and query the supported monitoring data. |
| `scheduleproof` | Experimental replicas require verified historical inputs, continuity and fresh evidence; restart/fork reconciliation fail closed. |
| `website` | Documentation output stays inside its build directory; routes and full documentation build remain valid. |
| `documentation` | Describe the verification boundary and preserve the recovery contract; documentation remains buildable. |
| `operations` | Release installation verifies bytes before installation; both binaries build; proofs and dependency execution remain containerized. |
| `law-system` | All declared laws have checked proofs and named evidence; orphaned subsystems, stale evidence and incorrect models fail the gate. |

## Maintenance and CI

- State new requirements in root `LAWS.bend`; implement the model and supply
  proofs in `PROOF.bend`. Treat edits that weaken laws as contract changes.
- Map new areas and named implementation tests in `test/bend/coverage.json`.
  Existing area patterns cover new files within that area; a new package needs
  its own area. New test names must be reflected in the map.
- Extend the direct comparisons when changing a modeled Go decision. A proof
  alone does not detect divergence in Go code.
- Run `make laws` before committing. The `Bend laws` workflow runs the same gate
  on every pull request and push to main, without path filters. It preserves
  failure logs for seven days. Require its `laws` job in branch protection if
  merges must be blocked; this change does not alter GitHub repository settings.

The website's generated documents and dependencies, compiled binaries, private
fixtures, `.git` and ignored local scratch files are not maintained source and
are excluded by Git's ignore rules. Checked-in test fixtures, dashboard data,
release configuration and experiment measurements remain in the inventory.
Live-network tests and interactive manual tests keep their existing opt-in
behavior; they are not selected as required contract evidence.

The model does not prove cryptographic primitives, filesystem crash durability,
operating-system behavior, network delivery, every concurrency interleaving,
visual appearance, or historical design proposals that are not implemented.
Bend's parser/translation, compiler/kernel and comparison adapters are trusted
components. The regular Go, website and operational checks remain necessary.
