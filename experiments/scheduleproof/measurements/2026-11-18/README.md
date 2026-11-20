# Mainnet sync measurements — November 18, 2026

These are bounded **early-mainnet replay** measurements, not full synchronization
or current-tip capacity estimates. The preserved replica advanced from genesis
to height 10,701. The sampled public frontier was approximately 14.686 million.
The runner correctly remained `catching-up` and did not publish a current schedule.

Linux arm64 containers ran on the local Colima VM. State was on the VM's native
filesystem in the existing `nodebox-npm-cache` volume, under
`/home/app/scheduleproof-mainnet-20261118`. The successful final run used the built
non-root service image, a read-only root filesystem, and a 2 GiB memory limit.
The bounded runs used 100-momentum batches and 300 ms minimum pauses. This is a
research replica; the documented Compose service uses its own dedicated volume.

| Run | Verified heights | Wall seconds | Process CPU seconds | Peak RSS MiB | Final data MiB | RPC payload MiB | Pre-apply matches |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: |
| [01-interrupted](01-interrupted.json) | 1 → 1,701 | 33.08 | 8.30 | 532.4 | 12.98 | 34.94 | 1,699/1,699 |
| [02-resume](02-resume.json) | 1,701 → 4,701 | 46.90 | 6.27 | 518.8 | 15.49 | 58.19 | 3,000/3,000 |
| [03-baseline](03-baseline.json) | 4,701 → 7,701 | 25.33 | 8.30 | 582.3 | 17.36 | 58.20 | 3,000/3,000 |
| [04-optimized](04-optimized.json) | 7,701 → 10,701 | 22.70 | 10.25 | 524.6 | 18.65 | 3.60 | 3,000/3,000 |

The first run stopped when the already-constrained container filesystem filled.
Its replica was only 13 MiB. Disposable build caches were cleared, preserving all
replica data, images, containers, and volumes. The next process resumed at height
1,701 against a second public source and verified the following 3,000 momentums.
That run overlapped test compilation; the baseline and optimized samples did not.

The optimization avoids repeatedly downloading the large genesis header.
Genesis is checked once at startup; every batch still checks chain/frontier
agreement and validates all replayed state. The final sample used 3.60 MiB of
RPC payload for 3,000 momentums. Ranges differ between runs, so their relative
throughput is observational, not a controlled benchmark of identical content.

Across these four runs, 10,700 momentums were replayed, with **10,699/10,699**
pre-apply schedule comparisons matching. One initial momentum lacked a covered
proof cutoff for that additional comparison; every momentum still passed the
engine verifier. This measures replay against historical production, not live
future-slot accuracy at today's tip. Empty-slot assignments are checked in the
controlled validator suite, not inferred from absent production.

Peak RSS is the process maximum, not idle memory. Data size is the sum of file
lengths after closing the replica, not filesystem allocated blocks. RPC bytes
count JSON request/response payloads, excluding transport framing. CPU seconds
are user plus system process time. Nothing here establishes full-chain disk
size, full bootstrap duration, modern transaction load, or near-tip catch-up
performance. A longer synchronization run is still needed for those results.

## Resume and lifecycle evidence

- Three successive process starts resumed the committed mainnet height, including
  the start following the full-disk failure.
- Controlled tests kill the process before and after an atomic commit, reopen
  the database, and validate all remaining captured mainnet momentums.
- Controlled source forks replace historical input state, invalidate old
  schedules, and match the replacement validator's slots. Deeper-than-configured
  forks retain the replica and refuse readiness; lagging sources never rewind it.
- Incomplete responses, cancellation, mid-read source changes, stale/future
  timestamps, engine/genesis mismatch, concurrent writers, and unrelated data
  directories are covered.
- Full race tests passed after adding atomic storage and lifecycle handling;
  the affected sync/race tests passed again after the genesis-download change.
  `go vet`, the executable build, image build, non-root read-only image smoke
  test, and Compose configuration validation passed.

No long-running watcher was left running after these bounded measurements.
The replica and reports are retained for further testing.
