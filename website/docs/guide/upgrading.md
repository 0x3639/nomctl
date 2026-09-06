---
title: Upgrading
description: "Upgrade nomctl in place with nomctl upgrade, roll back, and how status tells you when nomctl or go-zenon has an update."
---

## nomctl

```bash
sudo nomctl upgrade            # latest release
sudo nomctl upgrade --check    # only report
sudo nomctl upgrade --version v0.4.0
sudo nomctl upgrade --rollback # restore the previous binary
```

`upgrade` finds the latest GitHub release, downloads the archive for the host architecture together with `checksums.txt`, verifies the sha256, and replaces the running binary in one atomic rename. The old binary is kept as `nomctl.previous` next to it for `--rollback`. If `nomctl-alerts.service` is running it is restarted so the daemon uses the new code. Nothing else on the node is touched.

The installer one-liner does the same thing and remains a fine way to upgrade; `nomctl upgrade` just saves a trip to the README. `NOMCTL_REPO` (default `0x3639/nomctl`) selects the repository releases come from.

## The node

`nomctl deploy` rebuilds go-zenon from the head of the configured branch and restarts the service. It also rewrites the systemd unit if the one on disk differs from the current definition.

## Knowing when to upgrade

`nomctl status` and `top` check, at most every six hours, whether a newer nomctl release exists and whether the configured go-zenon branch has commits beyond the running node's build. When either is true a line is added:

```
Update    nomctl 0.5.0 available (running 0.4.0): sudo nomctl upgrade
Update    go-zenon master has new commits (deployed 1a2b3c4, remote 9f8e7d6): sudo nomctl deploy
```

Nothing is printed when up to date or when the check fails. The result is cached in `/run/nomctl/update-check.json`. `NOMCTL_UPDATE_CHECK=false` (or `status --no-update-check`) disables the check entirely for nodes that should not contact GitHub.

The same signal is available as the `update_available` alert, which is off by default:

```bash
sudo nomctl alerts enable update_available
```

It is informational: one Telegram message when an update appears, no reminders, and a "up to date" message after you upgrade or deploy.

## The relay

The relay is a container; upgrade it by redeploying the new image on Coolify. Pairings survive in the volume.
