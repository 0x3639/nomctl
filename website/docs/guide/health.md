---
title: Confirm health after changes
description: "Observe node progress and executable identity after upgrades and chain restores."
---

R4 is implemented locally, pending release. Source and signed-release upgrades, chain-backup restores, and restore-point restores now run a bounded health check after the change. Device migration uses the same check after handoff.

The default observation window is 30 seconds. Reports compare the service, heights, peers, restart count, RPC znnd version/commit, and installed/running executable SHA-256 before and after the change. Progress is measured between post-change observations of the same process; old heights from before a restore are not used as its progress baseline.

| Outcome | Meaning |
|---|---|
| Healthy | Service and RPC respond, peers are present, a recent frontier advances, and both executable hashes match the intended znnd. |
| Syncing | The node is still catching up, but measured progress, peers and executable identity are confirmed. This passes the check. |
| Stalled | No progress was observed or the reported frontier remains old. Inspect logs and repeat the check. |
| Failed | The service/RPC failed, peers are absent, the node restarted, heights regressed, or executable identity differs. |
| Unknown | Evidence was missing, ambiguous, interrupted, or insufficient. This is not reported as success. |

Checks establish observed operation during the window, not future uptime or database compatibility. A busy node may need another observation window. Run a standalone check without changing the node:

```bash
sudo nomctl health
sudo nomctl health --wait 60s --json
```

`--wait` accepts 1 second through 5 minutes. Samples are spaced five seconds apart, so very short windows can return unknown. Any outcome other than healthy or syncing exits nonzero.

Before restoring chain data, nomctl saves and verifies a protected restore point containing the current data and znnd. This adds a backup stop/start cycle and requires extra space before the requested restore. The existing moved-aside data protection is also retained. Upgrades retain their matching pre-upgrade restore point.

If a change fails, the error identifies the retained recovery point. Interactive use offers one separately confirmed recovery action. By default that restores the matching **data and saved znnd together**; an explicit data-only restore keeps that choice in guided recovery. No failure automatically downgrades just the binary, and a failed recovery does not start a rollback loop. Noninteractive commands report the recovery command and exit with an error.

Migration failures use the dedicated [return workflow](/guide/migration#interrupted-or-failed-migration) so the replacement is blocked before the original can restart.
