# R1: Check my node

## Operator workflow

Add `nomctl doctor [--json] [--wait 10s]` and **Manage node → Check my
node**. Collect two observations, then report stable check IDs, severity,
evidence, and suggested next steps. Diagnose service state/restarts, RPC,
chain progress, peers, disk space, clock synchronization/offset, P2P
listening, and known recent journal signatures. This is read-only: no
preflight repairs, service changes, operation lock, or firewall edits.
Unavailable system utilities and unreadable evidence become unknown findings
instead of preventing the rest of the report.

The report always includes znnd identity: RPC-reported version and commit,
SHA-256 of the configured installed executable, and SHA-256 of the running
process executable when available. Label these sources separately; a mismatch
means the running and installed files differ, not that either is authentic.
Do not execute znnd to discover its version. Recheck the process identity
after hashing and discard the running identity if the process changed.

## Evidence and limits

- Reuse the metrics sampler and node RPC client. Read HTTP/P2P settings from
  the configured data directory's `config.json`; missing settings use the
  existing schema defaults. Invalid settings are reported rather than replaced
  with guessed values. A custom HTTP bind address must be an IP or localhost.
- Default observation gap: 10 seconds; `--wait` accepts 1 second to 2 minutes.
  Each sample has a 10-second deadline. Recheck service PID, start time, restart
  count and state after its RPC calls; a change or unavailable identity query
  invalidates process continuity and height comparisons. Positive progress is evidence; a short
  unchanged height alone does not prove a stall. A stale synced frontier uses
  the existing two-minute threshold; syncing without observed progress is a
  warning to observe longer, not a conclusive failure. Decreasing heights,
  missing observations and restarts are reported separately.
- Local network evidence comes from Linux TCP listener tables and the node's
  socket descriptors. Report bindings and ownership separately from outside
  reachability. No public IP discovery or external probe runs by default.
- Optional CLI-only `--probe-url https://... --public-ip ADDRESS` sends the
  explicitly supplied public IP and configured P2P port to the selected
  endpoint. Explain the contact on stderr before requesting it (including JSON
  mode). The endpoint must implement the documented JSON contract, echo the
  target, and return a boolean reachability result. TLS verification remains
  enabled; redirects, credentials in URLs, and oversized responses are refused.
  Missing/malformed/unavailable probes mean unknown, never closed.
- Read kernel time synchronization and offset correction without contacting
  an NTP server or changing time settings. The offset is an estimate, not an
  independent wall-clock measurement. An unsynchronized clock, an offset over
  one second, or a frontier far in the future needs investigation.
- Disk checks use the configured data directory and the R2 saved backup
  destination when present, falling back to the configured backup directory.
  Keep inaccessible paths distinct from zero free bytes. Compare against
  `NOMCTL_MIN_FREE_SPACE_KB` and 90% utilization; do not prune anything.
- Search at most 1,000 service journal lines from the last hour, bounded to
  1 MiB and 5 seconds. Show known signature names and counts, not raw journal
  messages or secrets. Historical signatures are warnings, not proof that the
  current process remains broken. An empty journal is inconclusive.
- Commands and network reads are bounded and cancellable. File hashes are
  streamed with context checks and a 1 GiB cap; special files are refused.
  Checks may overlap maintenance; they never acquire the mutation lock.

## Output and failure handling

Text and JSON carry the same findings. JSON has `schema_version: 1`, timestamps,
identity, and checks. Severity is `ok`, `info`, `warning`, `error`, or `unknown`.
Exit 0 means the report was produced, even if it contains failures; exit 1
means invalid options, cancellation, or report-output failure. Scripts should
inspect check severity. Missing version/hash fields include a reason rather
than an inferred value. Text strips terminal control characters and bounds
untrusted fields. No diagnosis automatically starts recovery or removes data.

## Acceptance

Cover healthy/stopped/missing services, missing RPC and individual identity
calls, busy frontier, real progress, unchanged short samples, restart/reset,
low peers, zero/free/unreadable disk, unsynchronized clock, invalid/custom
ports, listener ownership, absent/failed/mismatched probes, log signatures
without secret disclosure, binary mismatch/replacement, cancellation, JSON,
and diagnostic command/menu wiring. Run the full test/lint/docs/build checks,
address PR feedback, merge, then tag v0.16.0 containing R1 and R2. Verify release
assets and checksums after the release workflow completes. Include the agreed
R11 migration roadmap clarification while updating delivery status.
