# Non-root alerts daemon

The `nomctl-alerts` service on each node runs `nomctl alerts run` as root.
It talks to the internet every 30 seconds and lives on the same machine as
the pillar's wallet. This change runs it as an unprivileged system user and
keeps every alert working, including `fds_high`, through a small root-only
probe.

## Runtime user

- A locked system user and group `nomctl` (no shell, no home) created by
  `nomctl alerts setup` and re-checked by every root `nomctl alerts` command
  and by `nomctl upgrade` (`alerts.Converge`).
- `/etc/nomctl/alerts.json`: root:nomctl 0640 (the pairing secret).
- `/run/nomctl`: `RuntimeDirectory=nomctl` owned by nomctl, 0755. The daemon
  writes `alerts-state.json` and the update cache there; root's `status`,
  `top` and the probe write there too. All writers create a private temp
  file and rename, so ownership of an existing file never blocks a writer.
- Logging: the daemon logs to the journal only; the unit does not set
  `NOMCTL_LOG_FILE`.

## Unit

```
[Service]
User=nomctl
Group=nomctl
NoNewPrivileges=true
CapabilityBoundingSet=
AmbientCapabilities=
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
ProtectKernelTunables=true
ProtectControlGroups=true
RestrictSUIDSGID=true
LockPersonality=true
RuntimeDirectory=nomctl
RuntimeDirectoryPreserve=yes
ReadWritePaths=/run/nomctl
```

`ProtectControlGroups` makes `/sys/fs/cgroup` read-only, which is all the
sampler needs. `ProtectHome=true` hides `/root`, so the data directory is
not reachable; see disk free below.

## What changes in the sampler

- **Disk free** for the data directory is measured on its mount point,
  found by the longest matching mount in `/proc/self/mountinfo`, instead of
  on the directory itself. `/root` is 0700 and hidden by `ProtectHome`.
- **Per-process open files and I/O bytes** of znnd need `CAP_SYS_PTRACE` to
  read from another user's `/proc/<pid>`. That capability also allows
  reading znnd's memory, so the daemon does not get it. Instead:

## The probe

`nomctl alerts probe` (root) reads znnd's `/proc/<pid>/fd` count, the
`NOFILE` limit and `/proc/<pid>/io`, and writes
`/run/nomctl/process-probe.json`:

```json
{"at":"2026-09-07T12:00:00Z","pid":1234,"open_fds":412,"fd_limit":32768,"read_bytes":1,"write_bytes":2}
```

`nomctl-alerts-probe.service` (oneshot, root, `ProtectSystem=strict`,
`ProtectHome=true`, `ReadWritePaths=/run/nomctl`) and
`nomctl-alerts-probe.timer` (`OnBootSec=30s`, `OnUnitActiveSec=30s`) run it.
Both are installed and removed with the daemon unit.

The sampler, when it cannot read those `/proc` files itself, uses the probe
file if it is under two minutes old and names the same pid; otherwise the
fields stay zero and `fds_high` reports nothing, exactly as today when the
process is absent. Root callers (`status`, `top`, support bundles) keep
reading `/proc` directly.

## Migration

`alerts.Converge` runs from `nomctl alerts setup|enable|disable|set` and
from `nomctl upgrade` whenever the daemon unit file exists (active or not). It ensures the user, file modes, the three unit
files and the timer, reloads systemd and restarts the daemon when a unit
changed. A node on an older release gets the new layout on its next
`nomctl upgrade` with no manual step. `alerts status` shows the user the
daemon runs as.

## Tests

Unit text and probe JSON round trip; mount-point lookup against a canned
`mountinfo`; sampler falls back to the probe only when `/proc` is
unreadable, fresh and pid-matched; Converge fixes modes and detects unit
drift; a container run as the unprivileged user with a fake systemd shows
heartbeats and alerts still flow and `fds_high` is fed by the probe.
