---
title: Roadmap
description: "A phased improvement roadmap for nomctl, informed by MyTonCtrl, Eth Docker, and Dappnode."
---

This roadmap prioritizes keeping a Zenon node healthy, making recovery dependable, and reducing routine maintenance. It adapts useful operator workflows from **TON’s MyTonCtrl**, **Eth Docker**, and **Dappnode** to nomctl’s single-node command-line and menu experience.

**Research reviewed: November 9, 2026.** The phases below are a proposed delivery order, not release-date commitments. Features outside the shipped baseline are planned work; suggested menu names are proposals. Each implementation starts with a design spec under `docs/superpowers/specs/` before code.

## What we already have

The baseline includes the backup and restore improvements in **v0.15.1**. Existing capabilities should be extended rather than rebuilt.

| Area | Shipped behavior | Remaining opportunity |
| --- | --- | --- |
| Node operation | Source deployment, source and signed upgrades, service controls, bootstrap, resync, and nomctl self-update | Diagnose failures and verify application health after changes |
| Recovery | Daily local backups; manual and pre-upgrade restore points containing chain data and znnd; saved version/commit when available and SHA-256; data-only or paired restoration | Verify stored recovery files periodically, improve schedule controls, and support off-machine copies |
| Retention and cleanup | Backups default to 7 days, maximum 30; 75% destination-filesystem guard; confirmed backup/restore-point cleanup | Show storage across recovery copies, logs, and chain data; forecast growth |
| Monitoring | `status`, live `top`, system metrics, sync/peer/Pillar information, and a Grafana/Prometheus installation workflow | Export nomctl’s node and recovery measurements for historical charts |
| Notifications | Telegram alerts for service, sync, peers, disk, memory, missed production, missing backups, and updates | Operation results, maintenance-aware behavior, and additional destinations |
| Configuration and support | Schema-aware config editing, saved previous config, encrypted wallet/config backups, and support bundles | Clear diagnosis, configuration comparisons, and guided migration |

The manual **Backup chain data locally** menu entry was removed in v0.15.1. **Schedule local backups**, **Create restore point**, **Restore from backup**, and **Restore from restore point** are the primary recovery choices. The backup command still supports scheduled and command-line runs.

## Useful patterns from other tools

These are selected strengths relevant to nomctl, not a claim that every tool implements the proposals below. The proposed acceptance criteria and ordering are nomctl-specific judgments.

| Reference | Documented strength | Adaptation for nomctl |
| --- | --- | --- |
| [MyTonCtrl core commands](https://docs.ton.org/nodes/cpp/mytonctrl/core) | Consolidated status, disk benchmarks, connectivity settings, and visibility into configured defaults/overrides | One guided diagnostic flow, an optional benchmark, and clearer effective settings |
| [MyTonCtrl recovery tools](https://docs.ton.org/nodes/cpp/mytonctrl/backups) | Validation of backup contents, recovery safeguards, and a standby-node procedure | Periodic verification and a rehearsable migration process; adapt to Zenon’s producer model |
| [TON operator guide](https://docs.ton.org/nodes/cpp/setup-mytonctrl) | Off-site recovery copies and regular node-health checks | Encrypted off-machine copies and a recurring recovery-readiness report |
| [MyTonCtrl metrics integration](https://docs.ton.org/nodes/cpp/integrating-with-prometheus) and [Eth Docker dashboards](https://ethdocker.com/Usage/Dashboards/) | Metrics integration and local or remote dashboard storage | A nomctl exporter and dashboard using the existing sampler and analytics stack |
| [Eth Docker networking](https://ethdocker.com/Usage/Networking/) and [resource guidance](https://ethdocker.com/Usage/ResourceUsage/) | Explicit networking requirements and measured client resource demands | External connectivity checks and workload-aware capacity advice |
| [Eth Docker storage management](https://ethdocker.com/Usage/NodeTypes/) | Storage behavior and maintenance depend on the client and its database mode | Explain storage use and offer supported cleanup; Ethereum pruning commands do not establish safe znnd pruning behavior |
| [Dappnode setup](https://docs.dappnode.io/docs/user/getting-started/setup/) and [notifications](https://docs.dappnode.io/docs/user/notifications/notifications-list/) | Operator controls for updates and visibility into system/application events | Opt-in maintenance windows, operation history, and result notifications |
| [Dappnode private access](https://docs.dappnode.io/docs/user/access-your-dappnode/vpn/overview/) | Private remote access to the management interface | Document secure access to the existing local dashboard first; consider a broader remote interface separately |

## Delivery sequence

Effort is relative: **S** means a focused change, **M** spans several components, and **L** needs integration work and recovery testing. These are planning estimates, not time promises. Dependencies refer to the IDs below.

| Phase | ID | Improvement | Effort | Depends on |
| --- | --- | --- | --- | --- |
| 1 — Reliable operation | R1 | Check my node: diagnostics and network checks | M | Existing sampler and support bundle |
| 1 — Reliable operation | R2 | Backup schedule status and controls | S–M | Existing cron scheduler |
| 1 — Reliable operation | R3 | Verify backups and restore points | M | Existing archive validation; R2 for scheduling |
| 1 — Reliable operation | R4 | Health checks after upgrade or restore | M | R1, R3 |
| 2 — Visibility and resilience | R5 | Node metrics exporter and dashboard | M | Existing analytics stack; R1/R2 result formats |
| 2 — Visibility and resilience | R6 | Storage breakdown and guided cleanup | M | R2/R3; R5 for historical growth |
| 2 — Visibility and resilience | R7 | Optional hardware and disk benchmark | M | R1, R6 |
| 2 — Visibility and resilience | R8 | Encrypted off-machine recovery copies | L | R2, R3 |
| 3 — Controlled maintenance | R9 | Operation history and maintenance notifications | M | R2, R4 |
| 3 — Controlled maintenance | R10 | Opt-in maintenance windows and update policy | L | R3, R4, R9 |
| 3 — Controlled maintenance | R11 | Migrate the entire `.znn` folder to a new device with rsync | L | R3, R4; SSH and rsync on both devices |
| 3 — Controlled maintenance | R12 | Configuration review and change history | M | Existing schema validation and config backups |
| 3 — Controlled maintenance | R13 | Recover a stalled or forked node by upgrading, rewinding and replaying | L | R1, R3, R4; supported offline rewind and production controls in znnd |
| 3 — Controlled maintenance | R14 | Delay pillar backups and restore-point creation around production slots | L | R1/R2/R4; verified replica bootstrap/catch-up, standalone calculator, producer identity |

Start with **R1, R2, and R3**. R2 is a useful smaller delivery while diagnostics and verification are developed. Complete R4 before adding unattended update policies.

**[v0.16.0](https://github.com/0x3639/nomctl/releases/tag/v0.16.0) delivery: R1 diagnostics and R2 backup controls.** R3 recovery-file verification, R4 post-change health checks, and R11 device migration are available for testing in **[v0.17.0-rc.1](https://github.com/0x3639/nomctl/releases/tag/v0.17.0-rc.1)**. Stable v0.17.0 awaits real-server migration and reboot acceptance. Device migration (R11) transfers the entire `.znn` folder directly with rsync over SSH; it does not depend on the off-machine archive-upload feature (R8).

**Current priority adjustment:** validate R11 on two servers, then design and implement R14 pillar-aware backup timing, followed by R13 stalled-node/fork recovery. R5 metrics remains next in the original phase sequence. R13 and R14 are design work and are not part of v0.17.0-rc.1.

## Phase 1: reliable operation

### R1 — Check my node

**Implemented for v0.16.0 ([PR #38](https://github.com/0x3639/nomctl/pull/38)).** `nomctl doctor [--json] [--wait 10s]`, also under **Manage node → Check my node**. Includes znnd version/commit and installed/running executable SHA-256. [Usage and evidence limits](./troubleshooting/doctor.md). Design: `docs/superpowers/specs/2026-11-17-node-diagnostics-design.md`.

Add a menu action that explains service failures, RPC failures, stalled chain progress, low peer counts, time drift, disk pressure, and known log signatures. Integrate the existing planned **Doctor** and **Network check** work into one operator flow.

Completion means the report shows evidence, severity, and a suggested next step for each finding, with text and JSON output. Network checks distinguish local listening from outside reachability on the configured P2P port (default 35995/TCP). They report **unknown**, not **closed**, when an external probe is unavailable. The first version diagnoses without editing firewall rules or restarting services. External probes are optional and disclose what endpoint is contacted.

### R2 — Backup status and schedule controls

**Implemented for v0.16.0 ([PR #37](https://github.com/0x3639/nomctl/pull/37)).** `backup status [--json]` and `backup schedule edit|pause|resume`, also available under **Backup status & schedule** in the menu. [Usage and retry policy](./guide/backups.md#status-edit-pause-and-resume). Design: `docs/superpowers/specs/2026-11-16-backup-schedule-controls-design.md`.

Show enabled/paused state, server timezone, next scheduled run, chosen retention, backup destination, last attempted run, last successful run, and failure reason. Add edit, pause, and resume actions so stopping future backups does not require deleting a cron file by hand.

Completion means status survives restarts and distinguishes success, failure, skipped-because-busy, and missed runs. Reconfiguration creates only one managed schedule, and pausing never deletes stored files. Show recovery points separately from rotating backups. Define catch-up and retry behavior in the design before implementing it; retries must respect the operation lock and disk guard.

### R3 — Verify backups and restore points

**Available for testing in v0.17.0-rc.1.** `nomctl verify` and **Backups & recovery → Verify recovery files** check archive integrity and saved znnd, persist results tied to file identities, support optional extraction rehearsal, and provide daily verification with pause/resume. [Usage and limits](./guide/backups.md#verify-recovery-files). Design: `docs/superpowers/specs/2026-11-17-recovery-verification-design.md`.

Add **Verify recovery files**, with an optional schedule. Reuse checksum and archive-entry validation; verify the saved znnd checksum for full restore points. Record the result, time, and exact artifact identity so an old verification result cannot be shown for a changed file.

Completion means truncation, bad checksums, unsafe archive entries, and missing components produce clear results without modifying live data or stopping znnd. Verification works on legacy metadata. Failed verification never deletes a restore point automatically. A separate optional extraction rehearsal must check temporary-space requirements first. Archive verification establishes file integrity, not database-version compatibility or guaranteed startup; the UI must distinguish those results.

### R4 — Confirm health after changes

**Available for testing in v0.17.0-rc.1.** [Health reports and recovery behavior](./guide/health.md).

Extend upgrade and restore completion beyond “systemd is active.” Observe RPC responsiveness, chain-height movement, peer state, repeated restarts, and the reported znnd identity over a bounded window.

Completion means a healthy but still-syncing node is distinguished from a stalled or failed one, and the report compares before/after observations. On failure, show the retained recovery point and offer a confirmed recovery action. Never automatically downgrade only the executable after the data may have migrated. Use the matching data/znnd pair for guided rollback and preserve the operator’s explicit data-only choice.

## Phase 2: visibility and resilience

### R5 — Export node and recovery metrics

Finish the existing metrics-exporter roadmap item. Feed Prometheus from nomctl’s sampler and supply a dashboard for chain height, peer counts, Pillar production, restart counts, backup age/results, verification age, and storage growth. Reuse the Grafana installation workflow already shipped.

Completion means stable metric names, units, bounded labels, documented collection intervals, and a working dashboard on a sample node. Bind locally by default and keep keys, passwords, and sensitive configuration out of metrics. Remote export is an explicit option. Separate unavailable measurements from zero values.

### R6 — Explain storage and clean up safely

Show space consumed by chain data, ordinary backups, protected restore points, restore safety copies, failed staging directories, logs, and other files. Explain which items are eligible for cleanup and estimate the space to be reclaimed.

Completion means cleanup previews exact paths and revalidates them under the operation lock. Protected points and recovery copies require explicit selection or confirmed UI pruning. Chain databases are never deleted as generic cleanup. A growth forecast states its observation window and uncertainty. Preserve the existing 75% backup-write guard; the forecast is advisory and does not replace per-write checks.

### R7 — Benchmark before deployment or during maintenance

Extend CPU/RAM preflight with an optional disk latency and throughput test, plus a readable hardware report. Use Zenon measurements to set guidance; do not transplant TON or Ethereum hardware thresholds.

Completion means the test uses a bounded temporary file on the intended filesystem, checks space before allocation, reports its expected workload, supports cancellation, and cleans up after failures. Stress tests require an explicit choice and are excluded from routine health checks. Record hardware, workload, and software version alongside results so comparisons are meaningful.

### R8 — Keep encrypted recovery copies off the node

Add a destination workflow for an operator-controlled remote server or object store. Start with one transport selected in the design, then add another if useful. Copy completed archives after the node is running; report local backup success and remote-copy success separately.

Completion means encryption before upload, protected credentials, interrupted-transfer handling, destination integrity verification, and a documented restore drill onto another machine. Wallet/config material remains separately encrypted and explicitly selected. Remote deletion must honor the same distinction between rotating backups and protected restore points; failed uploads never justify removing the only local recovery copy. The UI must explain who holds the recovery key and how to test it.

## Phase 3: controlled maintenance

### R9 — Operation history and maintenance notifications

Record upgrades, restores, scheduled backups, verification, and cleanup with start/end times, outcome, artifact IDs, version changes, and a concise failure reason. Add result notifications through the existing alert pipeline and then a generic webhook destination.

Completion means history is bounded, survives restarts, excludes secrets, and links failures to diagnostics or a support bundle. Planned maintenance may suppress expected service-down noise for a bounded period, but reports failed recovery and automatically ends suppression. Reuse existing mute behavior where appropriate. Extend host notifications with reboot-required and supported hardware-health signals only when the host can supply reliable evidence.

### R10 — Schedule maintenance deliberately

Offer explicit maintenance windows and update policies: notify only, prepare for review, or an opt-in unattended policy designed for verified releases. Start with preparing downloads/builds while the service runs and letting the operator approve installation.

Completion means pinned versions can be respected, pending changes and expected downtime are visible, and every installed node upgrade retains its required restore point and space check. No unattended source-branch upgrade by default. Unattended installation depends on proven health checks and a reviewed recovery policy; it must stop after a failed attempt rather than enter an upgrade/rollback loop.

### R11 — Migrate to a new device with rsync

**Available for testing in v0.17.0-rc.1.** [Migration workflow and supported installations](./guide/migration.md). Container rehearsals exercise rsync and controlled service failures; real-server deployment and reboot acceptance remain a stable-release check.

Add a **Migrate to a new device** menu action and a matching command-line workflow. Transfer the **entire `.znn` directory using rsync over SSH**, including chain databases, wallet/producer keys, `config.json`, hidden files, and any other contents. Use the configured node data directory (`NOMCTL_ZNN_DIR`, default `/root/.znn`) and an explicitly selected destination. This must not reuse the chain-backup folder allowlist, which would omit wallet/config and other files.

The first version uses a stopped-node transfer. Before downtime, check SSH access and host identity, rsync availability on both devices, destination space and permissions, and the target's architecture, znnd version, data path, ports, and service settings. Preview the two devices, exact paths, data size, and expected downtime. Prepare a compatible znnd and service on the destination without starting it; the executable and service definition normally live outside `.znn` and are not supplied by the directory copy. Retain a verified recovery point and a separate encrypted wallet/config backup before the handoff.

Completion means:

- Stop znnd on both devices and confirm it is inactive before copying. Hold the operation lock on both devices and prevent service auto-start and scheduled mutations during the transfer. The original producer must remain stopped, including after a reboot, before the replacement can start.
- Copy into a dedicated destination staging directory, including hidden entries, with progress and resumable interrupted transfers. Preserve file permissions, timestamps, and links, validate destination ownership for its service user, and identify links that depend on paths outside `.znn`. The first version refuses external symlinks: the operator must move the required contents inside `.znn` and adjust the link before migration, or defer migration. It never silently follows or copies an external target. Refuse to overwrite an existing destination data directory in the first version.
- Verify the completed directory against the stopped source, including file contents and required metadata, before putting it into service. Recheck link resolution and refuse any link that escapes the staged `.znn` tree, including dangling external targets; internal links must resolve within that tree after promotion. An interrupted or failed copy never starts the destination; report each node's state and provide an explicit resume or recovery path. Never automatically restart the original when the replacement may be active.
- Start the replacement only after the producer handoff is confirmed, then check RPC, sync progress, peers, and producer configuration using R4. Keep the original `.znn` directory intact after success; source cleanup is a separate, explicit operator action. Returning to the original requires first confirming the replacement is stopped and disabling its automatic starts, including at boot, so it stays inactive through a reboot before the original can restart.
- Protect wallet/config material through SSH transport and restrictive destination permissions; do not expose secrets in progress output or logs. Explain that copying `.znn` does not migrate external backup directories, alerts configuration, backup schedules, or orchestrator data; list the setup actions needed on the new device.

Rehearse the workflow on a second machine, including transfer interruption/resume, insufficient space, verification failure, an existing destination, and failed destination startup. Cover external symlinks with absent or differing destination targets: both must block migration and destination startup until the dependency is brought inside `.znn`. Rehearse returning to the original and rebooting the replacement to confirm it stays inactive. R3 supplies recovery-file verification and R4 supplies post-migration health checks; R8 is not a prerequisite for direct device-to-device transfer. Automatic failover and multi-node management remain outside this delivery.

### R12 — Review configuration changes

Extend the existing config editor with a readable before/after comparison, effective values versus defaults, and a browsable history of validated changes. Show which changes require a restart.

Completion means secrets are redacted, malformed changes are refused, and reverting a previous configuration is validated against the installed znnd schema. Configuration changes remain distinct from data/znnd restore points, with an explicit restart choice.

### R13 — Recover a stalled or forked node

**Designing; not implemented.** Support replaying 5, 10 or a checked custom number of recent momentums, and fork recovery to a known-good common ancestor height/hash. The intended workflow is: verify the updated go-zenon/znnd build, stop production, preserve a verified recovery point, rewind offline, replay on the intended branch, then explicitly resume production after branch and health checks pass.

The update and rewind share one maintenance window; the producer must not start between them. Fork recovery also needs an expected checkpoint and a reviewed reconnection plan so advancing on the wrong branch cannot be reported as success. This changes local node state, not network history.

Current znnd has internal rollback machinery but the examined release does not expose an administrative rewind command. A supported, version-matched engine interface and a persistent non-producing replay mode are prerequisites. Available history and tested engine limits bound custom depth; there is no promise of unlimited rollback. Design: `docs/superpowers/specs/2026-11-17-chain-recovery-design.md`.

The next implementation slice is an offline inspector in go-zenon, followed by candidate rewind and controlled replay. The proposed engine contract also requires production to be disabled before key loading and peer/checkpoint restrictions during fork recovery; changing seed nodes alone does not restrict discovery. Interface and acceptance details: `docs/superpowers/specs/2026-11-17-rewind-engine-contract.md`.

### R14 — Protect pillar production during backups

**Read-only validation stage; next priority after migration acceptance. Not included in v0.17.0-rc.1.** Delay chain backups and restore-point creation until this pillar has a sufficiently long gap in its upcoming production schedule. Confirmed non-pillar nodes keep their existing behavior.

Identify the running producer address and resolve its pillar name from authoritative node/chain evidence; do not rely on the alerts label or a wallet owner's address. Show upcoming timestamped production slots, the next assignment, the required stopped interval, and the reason a backup is waiting. The interval must include stop, copy, restart, peer reconnection/catch-up, and timing headroom. Compression continues after restart.

Calculate the schedule in a standalone runtime service using existing detailed-ledger RPCs and a pinned go-zenon implementation. No new go-zenon endpoint or engine modification is planned. The examined engine computes 30 ten-second slots per election round. The [Zenon Observatory backend](https://github.com/0x3639/go-zenon-version/blob/7ade222f04da480167f704833607d5c74abebafc/backend/schedule.go) demonstrates standalone projection, but its current cached weights are insufficient for exact historical inputs: controlled transitions produce incorrect assignments.

**Historical inputs and read-only sync validated.** A separate verified replica matches the validator's complete inputs and all slots across registration/revocation, producer changes, delegation/ranking and balance changes, proof boundaries, and forks. The standalone runner now has atomic commits, resumable synchronization, bounded fork reconciliation, expiration of schedule output, and resource reports. Tests cover process kills around commits, source interruption/changes, 35 candidates, cache refresh, incomplete history, and restart. Container packaging and a separate CI job are included.

Bounded mainnet runs reached height 10,701 with 10,699/10,699 pre-apply schedule comparisons matching. The final 3,000-momentum run took 22.7 seconds, peaked at 524.6 MiB RSS, and left an 18.65 MiB database. These are early-history samples, not full-chain capacity or current-tip qualification. Full synchronization and live schedule checks remain before backup integration. The watcher may run continuously or start ahead of a backup while retaining its database; on-demand starts must finish catch-up before a future coordinator accepts a window. Automatic backup-triggered startup is not implemented. Validation: `docs/superpowers/specs/2026-11-17-schedule-proof-validation.md`; measurements: `experiments/scheduleproof/measurements/2026-11-18/README.md`.

The implemented runner publishes a read-only schedule/status file. The planned backup integration will add an eligible/wait/unknown window decision for nomctl's producer and full outage budget. Nomctl retains service control and validates identity, schedule coverage, chain/proof agreement, freshness, and clock evidence immediately before stopping. A fresh feed or perfect recent match count alone is insufficient; unverified inputs or mismatches mean defer. Producer identity comes from reconciled local service/configuration and on-chain evidence; do not assume an existing RPC reports the loaded identity.

Waiting must leave the node running, allow cancellation and schedule pause/edit, and avoid holding the global operation lock for an entire round. Revalidate the producer and schedule under the lock immediately before stopping. Abort a late copy early enough to reserve time for restart and production readiness, and never mark an incomplete backup complete. If the data cannot fit any known gap, keep deferring and explain the required capacity or snapshot-method change.

This prevents knowingly scheduling a backup over an assigned production slot; it cannot guarantee zero missed momentums during arbitrary disk stalls, node failures, clock changes, or forks. Report actual readiness and production outcomes. Automatic restore points taken inside longer upgrades/restores need their entire maintenance interval accounted for; the guard must not imply that a copy-only window protects a longer operation. Design and acceptance cases: `docs/superpowers/specs/2026-11-17-pillar-backup-protection-design.md`.

## Rules carried forward

- **Backups** are rotating chain-data archives: daily scheduling, default 7 days, maximum 30, with oldest-first space pruning.
- **Restore points** preserve chain data and znnd. Days mark eligibility for manual pruning; indefinite points require individual deletion. No future scheduler, uploader, or cleanup feature may silently convert them into automatically expiring backups.
- **Restore from backup** changes data only. Restore-point recovery offers the saved znnd or an explicit data-only path and clearly explains compatibility.
- Check space before downtime, restart before compression where possible, and reuse the shared operation lock. Long archive uploads, routine archive verification, and benchmarking must not accidentally extend the stopped-service interval. R11 explicitly includes the directory transfer and final verification in its planned downtime.
- Retain separate encrypted wallet/config backups. R11 also transfers the live wallet/config as part of the complete `.znn` directory over SSH, with restrictive destination permissions. New automation is opt-in and must expose its status, failures, and disable control.

## Backlog and boundaries

Carry forward the lower-priority `nomctl pillars` overview and additional orchestrator controls once the recovery and diagnostic work is stable. Secure remote-access documentation can accompany the metrics work; a remote administrative UI would need a separate authentication and exposure design.

TON-specific staking elections, nominator pools, governance, and account operations do not transfer to Zenon node management. Account/governance functions remain with `znn-cli`. Multi-node remote control, centralized fleet telemetry, automatic producer failover, and unsupported chain-database pruning are outside these delivery phases.

## How an item becomes shipped

For each roadmap item, write a design with user-facing behavior, dependencies, failure/recovery handling, and acceptance checks. Implement one focused PR at a time, test the meaningful failure paths, update the guide and command reference, and link the merged PR and release here. Reassess ordering after Phase 1 using actual support incidents, backup/restore results, and operator feedback.
