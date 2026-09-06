---
title: Differences from the bash toolkit
description: "How nomctl differs from the hypercore-one bash toolkit it replaces."
---

nomctl reproduces the behaviour of [hypercore-one/deployment](https://github.com/hypercore-one/deployment) with these deliberate changes.

## Removed

- Only Zenon Network nodes are supported; the second node type and its arguments, variables and dashboard were removed.
- Modes, remote control and fleet telemetry from other node controllers are out of scope.

## Changed

- `arm64` is supported in addition to `amd64`; Go, node_exporter and Prometheus downloads are chosen per architecture.
- Environment variables use the `NOMCTL_` prefix instead of `ZNNSH_`.
- The Go toolchain and source checkout live under `NOMCTL_WORK_DIR` (`/opt/nomctl`) instead of the script directory; the log file is `/var/log/nomctl.log`.
- Scheduled backups use a systemd timer instead of cron, can be installed non-interactively with `backup --schedule`, and the unit carries the settings in effect when created.
- `logs` prints the last 20 lines by default; `-f` follows.
- Downloads, checksums and Grafana API calls are done in Go, so `curl`, `wget`, `jq` and `gpg` are not installed. The Grafana apt key is stored as `/etc/apt/keyrings/grafana.asc`.
- `deploy` installs `git`, `make` and `gcc` and refreshes the apt index before installing anything.
- In the menu, the repository and branch are chosen before dependencies are installed.
- Backup and restore honour `NOMCTL_ZNN_DIR`; the bash scripts hard-coded `/root/.znn`.
- A backup whose data copy fails restarts the node before reporting the error.
- Archives are published atomically with their sha256 sidecar; archives without one are ignored.
- Before a restore, the safety move of current data must succeed or the restore aborts.
- `resync` fails when a directory cannot be deleted instead of reporting success.
- Destructive operations abort when `systemctl` cannot report the service state, and are mutually exclusive through a lock file.
- The Grafana dashboard import references the Infinity datasource by its real UID; the bash version bound panels to a non-existent datasource.
- The node unit no longer runs `pkill -9 znnd` on stop; systemd terminates the service's own cgroup, and `deploy` rewrites a unit that differs from the current definition.
- Grafana is bound to localhost by default and a non-default `NOMCTL_GRAFANA_ADMIN_PASSWORD` is applied to Grafana on install.
- `analytics install` converges: every step checks its own precondition, so an interrupted run is completed on the next run.
- The pre-flight Internet check uses a TCP connection to `1.1.1.1:443` instead of ICMP `ping`.
- The restore picker lists the 20 newest archives.
- Interactive inputs are trimmed of whitespace; out-of-range environment values are rejected when used, so a valid flag can override them.

## Added

- v0.2.0: `status`, `top` and `support-bundle`.
- v0.3.0: Telegram alerts through a shared relay.
- v0.4.0: `momentums_stalled` and `pillar_missed` alerts, pillar production in `status` and `top`.
