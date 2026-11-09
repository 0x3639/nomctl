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
| 3 — Controlled maintenance | R11 | Guided migration to a replacement node | L | R3, R4, R8 |
| 3 — Controlled maintenance | R12 | Configuration review and change history | M | Existing schema validation and config backups |

Start with **R1, R2, and R3**. R2 is a useful smaller delivery while diagnostics and verification are developed. Complete R4 before adding unattended update policies.

## Phase 1: reliable operation

### R1 — Check my node

Add a menu action that explains service failures, RPC failures, stalled chain progress, low peer counts, time drift, disk pressure, and known log signatures. Integrate the existing planned **Doctor** and **Network check** work into one operator flow.

Completion means the report shows evidence, severity, and a suggested next step for each finding, with text and JSON output. Network checks distinguish local listening from outside reachability on the configured P2P port (default 35995/TCP). They report **unknown**, not **closed**, when an external probe is unavailable. The first version diagnoses without editing firewall rules or restarting services. External probes are optional and disclose what endpoint is contacted.

### R2 — Backup status and schedule controls

Show enabled/paused state, server timezone, next scheduled run, chosen retention, backup destination, last attempted run, last successful run, and failure reason. Add edit, pause, and resume actions so stopping future backups does not require deleting a cron file by hand.

Completion means status survives restarts and distinguishes success, failure, skipped-because-busy, and missed runs. Reconfiguration creates only one managed schedule, and pausing never deletes stored files. Show recovery points separately from rotating backups. Define catch-up and retry behavior in the design before implementing it; retries must respect the operation lock and disk guard.

### R3 — Verify backups and restore points

Add **Verify recovery files**, with an optional schedule. Reuse checksum and archive-entry validation; verify the saved znnd checksum for full restore points. Record the result, time, and exact artifact identity so an old verification result cannot be shown for a changed file.

Completion means truncation, bad checksums, unsafe archive entries, and missing components produce clear results without modifying live data or stopping znnd. Verification works on legacy metadata. Failed verification never deletes a restore point automatically. A separate optional extraction rehearsal must check temporary-space requirements first. Archive verification establishes file integrity, not database-version compatibility or guaranteed startup; the UI must distinguish those results.

### R4 — Confirm health after changes

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

### R11 — Move to replacement hardware

Provide a guided export/import checklist that combines a verified recovery point, separately encrypted wallet/config backup, version details, and destination checks. Revalidate architecture, paths, ports, and service settings on the target machine; a saved executable may not run on a different architecture.

Completion means the procedure is rehearsed on a second machine and has an explicit producer handoff: the original producer is stopped and confirmed inactive before enabling the replacement. Do not copy TON-specific election timing or assume two active producers are safe. The first delivery is an operator-driven migration, not automatic failover or a fleet controller.

### R12 — Review configuration changes

Extend the existing config editor with a readable before/after comparison, effective values versus defaults, and a browsable history of validated changes. Show which changes require a restart.

Completion means secrets are redacted, malformed changes are refused, and reverting a previous configuration is validated against the installed znnd schema. Configuration changes remain distinct from data/znnd restore points, with an explicit restart choice.

## Rules carried forward

- **Backups** are rotating chain-data archives: daily scheduling, default 7 days, maximum 30, with oldest-first space pruning.
- **Restore points** preserve chain data and znnd. Days mark eligibility for manual pruning; indefinite points require individual deletion. No future scheduler, uploader, or cleanup feature may silently convert them into automatically expiring backups.
- **Restore from backup** changes data only. Restore-point recovery offers the saved znnd or an explicit data-only path and clearly explains compatibility.
- Check space before downtime, restart before compression where possible, and reuse the shared operation lock. Long uploads, verification, and benchmarking must not accidentally extend the stopped-service interval.
- Retain separate encrypted handling for wallet/config secrets. New automation is opt-in and must expose its status, failures, and disable control.

## Backlog and boundaries

Carry forward the lower-priority `nomctl pillars` overview and additional orchestrator controls once the recovery and diagnostic work is stable. Secure remote-access documentation can accompany the metrics work; a remote administrative UI would need a separate authentication and exposure design.

TON-specific staking elections, nominator pools, governance, and account operations do not transfer to Zenon node management. Account/governance functions remain with `znn-cli`. Multi-node remote control, centralized fleet telemetry, automatic producer failover, and unsupported chain-database pruning are outside these delivery phases.

## How an item becomes shipped

For each roadmap item, write a design with user-facing behavior, dependencies, failure/recovery handling, and acceptance checks. Implement one focused PR at a time, test the meaningful failure paths, update the guide and command reference, and link the merged PR and release here. Reassess ordering after Phase 1 using actual support incidents, backup/restore results, and operator feedback.
