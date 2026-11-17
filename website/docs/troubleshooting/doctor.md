---
title: Check my node
description: "Read-only node diagnostics with evidence, suggested actions, and znnd version and executable hashes."
---

Open **Manage node → Check my node**, or run:

```bash
sudo nomctl doctor
sudo nomctl doctor --json
sudo nomctl doctor --wait 30s
```

The report checks service state and restarts, RPC responsiveness, chain progress,
peers, disk space, clock synchronization, local P2P listening, and common recent
journal errors. Each finding includes evidence, severity, and a suggested next
step. It does not restart services, change the firewall, repair time settings,
delete data, or acquire the lock used by maintenance operations.

The default observation gap is 10 seconds; `--wait` accepts 1 second through
2 minutes. Collection adds time for bounded RPC/system queries and executable
hashing. Ctrl+C cancels. A short unchanged height does not prove a stall;
observe longer with `nomctl top` when the report recommends it. Recent log
signatures describe historical events that may already be resolved.

## znnd version and hashes

Both text and JSON reports show:

- **Version and commit** reported by znnd's `stats.processInfo` RPC method.
- **Installed SHA-256** of `NOMCTL_INSTALL_DIR/NOMCTL_BINARY_NAME`.
- **Running SHA-256** of the process executable identified by the configured
  systemd service, with its actual executable path.

Different installed/running hashes produce a warning: an upgrade may be waiting
for a restart, or the service may use a different file. Review the paths before
restarting. A file replaced while its old process is still running may show
`(deleted)` in the running path. Hashes identify contents; they do not authenticate
a release or replace signature verification.

If RPC is unavailable, its version/commit remain unknown; the installed file can
still be hashed. No version is inferred from the filename and znnd is never
executed to discover it. Unreadable, changing, non-regular, or over-1-GiB
executables have an unknown hash with a reason. If the process changes during
collection, its running hash is discarded and the report recommends repeating.

## Network and clock evidence

HTTP RPC and P2P ports come from the configured `.znn/config.json`; missing
settings use the node schema defaults (35997 HTTP, 35995 P2P). Wildcard HTTP
bindings are queried through loopback. Invalid settings are reported without
contacting a guessed endpoint. A custom HTTP host must be an IP address or
`localhost`. Disabling HTTP RPC leaves the RPC-based checks unavailable.

Local P2P checks inspect Linux TCP listeners and socket ownership for the node
PID. A local listener does **not** establish outside reachability. By default,
outside reachability is **unknown**, and no external probe or public-IP
discovery service is contacted. Peer direction is not inferred from peer counts.

Clock evidence comes from the kernel's synchronization state and last offset
correction. This is an estimate, not an independent measurement of wall-clock
accuracy. The check warns when synchronization is absent or the correction
exceeds one second. It does not contact an NTP server or change clock settings.

## Optional external probe

If you operate or trust an HTTPS service implementing the contract below:

```bash
sudo nomctl doctor \
  --probe-url https://your-probe.example/check \
  --public-ip 203.0.113.10
```

Replace the example endpoint and address with your actual probe and node IP.
There is no bundled public probe service. Both flags are required. Before
collection, nomctl prints the endpoint and target on stderr, including in JSON
mode. The endpoint receives the supplied IP and the configured P2P port, and
also sees the HTTP connection's source IP. No wallet, configuration contents,
peer list, or log messages are sent.

The request is `GET /check?address=203.0.113.10&port=35995`. The selected service
must attempt a TCP connection **from its own external network** and return HTTP
200 with this JSON, echoing the requested address and port:

```json
{"address":"203.0.113.10","port":35995,"reachable":true}
```

A valid `false` result means that probe could not connect; check the IP and
firewall/NAT rules and confirm from another network. Missing/malformed responses,
timeouts, HTTP errors, and target mismatches mean **unknown**, never "closed."
HTTPS certificate verification is required. Redirects, URL credentials, query
strings/fragments in the configured endpoint, and responses above 4 KiB are
refused. No external probe runs from the menu's default check.

## Interpreting the report

Severity is `ok`, `info`, `warning`, `error`, or `unknown`. Unknown evidence is
not counted as healthy. Disk checks inspect the data directory and the saved
R2 backup destination (including paused schedules); without a saved schedule,
they use `NOMCTL_BACKUP_DIR`. They warn below `NOMCTL_MIN_FREE_SPACE_KB`, at 90%
filesystem use, or with zero free space. Unreadable/missing directories are
unknown. No files are pruned.

Journal checks read at most 1,000 service messages from the last hour, bounded
to 1 MiB and 5 seconds. Only known signature names and counts are reported;
raw messages and credentials are omitted. Application log files and older
events may hold more evidence; use a [support bundle](./support-bundle.md)
when needed.

`--json` writes only the report to stdout. Schema version 1 includes
`started_at`, `finished_at`, `service`, `identity` (`rpc_version`, `rpc_commit`,
`installed`, `running`), and `checks`. Each check has a stable `id`, `severity`,
`summary`, `evidence`, and `next_step`. Unavailable version fields are omitted;
unavailable hashes have an `error` reason.

Exit **0** means the report was produced, including reports with warnings or
errors. Scripts should inspect check severities. Exit **1** means invalid
options, cancelled collection, or a report-output failure. Diagnostics remain
available during an outage and report partial evidence when system tools fail.
