# Troubleshooting: live node view and support bundle

Status: draft for review. Target release: v0.2.0.

## Goals

1. Let an operator see, in the terminal, what the node and its host are doing
   right now: sync state and speed, peers, CPU, memory, open files, disk,
   restarts. (`nomctl top`, `nomctl status`)
2. Let an operator produce one file that contains everything a helper needs to
   diagnose a crash or a stuck node, safe enough to share after a glance.
   (`nomctl support-bundle`)
3. Reuse one set of measurements for both, so the numbers in a bundle are the
   numbers the operator saw on screen.

## Non-goals

- No uploading of bundles anywhere. The operator shares the file.
- No changes to the node's `config.json`. znnd enables HTTP RPC on
  `0.0.0.0:35997` by default with the `stats` and `ledger` modules, which is
  all that is needed.
- No Grafana changes. The existing analytics stack remains the option for
  history; this feature is the option for "now".
- No historical storage on the nomctl side beyond the in-memory window `top`
  needs for rates.

## Command surface

| Command | What it does |
|---|---|
| `nomctl status [--json]` | Print one sample as aligned text, or as JSON, and exit. Exit 0 even when the node is down; the output says so. |
| `nomctl top [--interval 2s]` | Full-screen dashboard refreshed every interval. `q`, `Esc` or Ctrl+C exits. |
| `nomctl support-bundle [flags]` | Collect diagnostics into a directory and a `.tar.gz`, print both paths. |

`support-bundle` flags, mirroring the script:

| Flag | Default | Meaning |
|---|---|---|
| `--output DIR` | `/root/nomctl-support-<host>-<UTC stamp>` | Diagnostics directory; the archive is `DIR.tar.gz` |
| `--since TEXT` | `12 hours ago` | Journal window, passed to `journalctl --since` |
| `--watch` | off | Sample until systemd restarts the service, then collect |
| `--poll DURATION` | `10s` | Sampling interval in watch mode |
| `--timeout DURATION` | `0` (none) | Stop watching after this long |

All three require root (they read the journal, `/proc/<pid>` of a root
process and cgroup files) and take the usual `NOMCTL_*` configuration.
`status` and `top` do not take the node-data lock; `support-bundle` does not
either, because it never modifies node data. None of them run the pre-flight
checks (`--skip-preflight` is implied): a diagnostic command must not fail
because the NTP config is off.

The interactive menu gains two entries, between `monitor` and `resync`:
`status → Live node dashboard` (opens `top`) and
`support → Create a support bundle`.

## Architecture

Two new packages supply data; three consumers render it.

```
internal/node      JSON-RPC client for znnd (stats.*, ledger.getFrontierMomentum)
internal/metrics   Sampler: /proc, cgroup v2, systemctl show, node client -> Sample
                        │
        ┌───────────────┼──────────────────┐
   cmd/status      internal/tui/top    internal/support (bundle, --watch)
```

### internal/node

A minimal JSON-RPC 2.0 client over HTTP (`net/http`, no third-party
library) against `http://127.0.0.1:35997`. The port is not configurable in
this version (znnd's default; an operator who moved it can use the analytics
stack). Timeout 3 seconds per call.

Methods and the fields kept:

| RPC | Fields used |
|---|---|
| `stats.syncInfo` | `state` (0 unknown, 1 syncing, 2 done, 3 not enough peers), `currentHeight`, `targetHeight` |
| `stats.networkInfo` | `numPeers`, `peers[].ip`, `peers[].publicKey`, `peers[].name` |
| `stats.processInfo` | `version`, `commit` |
| `stats.osInfo` | `numGoroutine` |
| `ledger.getFrontierMomentum` | `height`, `timestamp`, `hash` |

Errors are returned, never logged, so consumers decide how to show an
unreachable node.

### internal/metrics

`Sample` is the single struct everything consumes:

```
Sample
  Taken           time.Time
  Service         ActiveState, SubState, MainPID, NRestarts, ExecMainStartTimestamp, Result
  Process         present bool; CPUPercent; RSS bytes; Threads; OpenFDs; FDLimit;
                  VmSwap; IO read/write bytes; cgroup memory.current/peak/max, pids.current
  Host            Load1/5/15; MemTotal/Available; DataDirFree/Total bytes;
                  Pressure cpu/io/memory "some avg10"
  Node            reachable bool; error string; SyncState; CurrentHeight; TargetHeight;
                  NumPeers; Version; Commit; Goroutines; FrontierHeight; FrontierTime
```

Sources:

- Service: `systemctl show <unit> -p ...` (one call, parsed key=value).
- Process: `/proc/<pid>/status`, `/proc/<pid>/stat` (utime+stime for CPU %,
  computed from the previous sample's values and wall-clock delta; the first
  sample reports 0), `/proc/<pid>/io`, `/proc/<pid>/limits` (open files
  limit), `ls /proc/<pid>/fd` count, `/sys/fs/cgroup<ControlGroup>/…`.
- Host: `/proc/loadavg`, `/proc/meminfo`, `Statfs` on the data directory,
  `/proc/pressure/{cpu,io,memory}` (absent on older kernels: left zero).
- Node: the five RPC calls above, made in parallel with one shared timeout.

`Sampler` keeps the previous sample for CPU % and a ring of the last 30
`(time, currentHeight)` pairs for the sync rate. Derived values, computed by
the sampler so all consumers agree:

- `MomentumsPerSecond`: linear rate over the ring window (needs ≥2 points).
- `ETA`: `(target-current)/rate` when syncing and rate > 0, else unknown.
- `FrontierAge`: `now - FrontierTime`. Flagged as stalled when the node
  reports sync done and the age exceeds 2 minutes (momentums are ~10 s apart).

Everything in this package is Linux-only in practice but must compile
everywhere; readers take a root path so tests can point them at fixture files.

### cmd/status

Text layout (values illustrative):

```
Service   go-zenon active (running), pid 1234, 0 restarts, up 3d 4h
Node      znnd v0.0.7 (a1b2c3d), syncing 1,234,567 / 2,000,000 (61.7%), 5.2 mom/s, ETA 1d 16h
Peers     14 connected
Frontier  height 1,234,567, 3s ago
Process   cpu 42.0%, rss 1.9 GiB, threads 38, open files 412 / 32768
Host      load 1.20 0.90 0.80, mem 3.1 / 7.8 GiB available, /root/.znn 210 GiB free
Pressure  cpu 2.1%, io 15.4%, mem 0.0%
```

When the node is unreachable the Node/Peers/Frontier lines read
`node rpc unreachable at 127.0.0.1:35997: <error>`. `--json` prints the
`Sample` struct with the derived values included.

### internal/tui/top

A bubbletea program in the alternate screen, refreshing every interval.
Layout: three bordered panels stacked vertically (Node, Process, Host) using
the existing `ui` styles, a progress bar for sync, a sparkline (from
`bubbles`) of momentums per second over the ring window, and a footer with
last-sample time and the exit keys. Narrow terminals fall back to the same
content without the sparkline. Errors from the sampler are shown in the panel
they belong to and the loop keeps running; the previous good value is not
kept, so a dead node is visible immediately.

Sampling runs in a goroutine started by `tea.Tick`; the UI never blocks on
RPC.

### internal/support

`Collect(cfg, Options) (dir, archive string, err)` writes these files, in
the script's numbering so people used to it find things where they expect:

| File | Content |
|---|---|
| `00-live-journal.log` | `journalctl -fu <unit>` captured during `--watch` only |
| `01-runtime-watch.log` | one `Sample` (text form) per poll during `--watch`, plus the last one after restart |
| `02-summary.txt` | host, time, nomctl version, unit, data dir, since window, `uname -a`, uptime |
| `03-service-status.txt` | `systemctl status --full --no-pager` |
| `04-service-properties.txt` | `systemctl show` with the script's property list, redacted |
| `05-service-unit.txt` | `systemctl cat`, redacted |
| `06-service-journal.log` | `journalctl -u <unit> --since` |
| `07-kernel-journal.log` | `journalctl -k --since` |
| `08-system-warnings.log` | `journalctl -p warning..alert --since` |
| `09-live-process.txt` | full `/proc/<pid>/{status,limits,io,cgroup}`, exe path, fd count |
| `09-cgroup.txt` | cgroup files as in the script |
| `10-host-resources.txt` | `free`, meminfo, pressure, `df -hT`, `df -i`, top 50 by RSS, `ulimit -a` |
| `11-app-log-inventory.txt` | listing of `<data dir>/log`, newest first |
| `12-app-log-tails/NN-<name>.tail` | last 4 MiB of the 30 newest log files, gzip handled |
| `13-crash-markers.log` | the script's regex applied to 06, 07 and 12 |
| `14-coredumps-list.txt`, `15-coredumps-info.txt` | `coredumpctl` for znnd, or "not installed" |
| `16-binary.txt` | exe path, `stat`, sha256, `go version -m` when Go is available |
| `17-oom-and-boots.txt` | `journalctl --list-boots`, `systemd-oomd` status, `oomctl` |
| `18-node-rpc.json` | `syncInfo`, `networkInfo` (peer IPs replaced by `"<redacted>"`, count kept), `processInfo`, `osInfo`, frontier momentum; or the RPC error |
| `19-nomctl.txt` | nomctl version, effective config via `Redacted()`, backup timer state (`systemctl list-timers`), last 200 lines of `/var/log/nomctl.log` |
| `20-status.txt` | one `nomctl status` text sample taken at collection time |

Rules carried over from the script: never read `config.json` or the wallet
directory; apply the same two redaction regexes to 04, 05 and 19; directory
created with mode 0700, archive 0600; every capture is best-effort (a failed
command writes its error into the file and collection continues); the end of
the run prints the directory, the archive, the crash-marker file and the
warning about addresses and paths.

`--watch` is the script's loop rewritten on the sampler: record
`NRestarts` and `MainPID`, sample every `--poll`, stop when `NRestarts`
grows or the PID changes after having been 0, on Ctrl+C, or on
`--timeout`; then wait 2 s, take a final sample, and run the full
collection. The live journal follower is a child `journalctl -fu` process
writing to file 00, stopped with SIGTERM.

### Menu

`internal/tui` gets `status` (runs the top program) and `support` (runs
`support.Collect` with defaults, then prints the paths). Both use the
existing "Return to main menu?" flow.

## Error handling

- Node unreachable: `status`, `top` and the bundle all say so and carry on
  with host and process data. Never a fatal error.
- Service missing: `status` prints `service go-zenon: not found`; `top`
  shows it in the panel; the bundle still collects host data.
- `support-bundle` never modifies anything under the data directory and
  never stops or starts the service. It is safe to run on a live node.
- Any single capture failing is recorded in its file; the command fails
  only if the output directory or the archive cannot be written.

## Testing

- `internal/node`: an `httptest` server answering the five methods, plus
  malformed and error responses.
- `internal/metrics`: fixture files for `/proc/<pid>/status`, `stat`, `io`,
  `limits`, `loadavg`, `meminfo`, `pressure/*`, cgroup files; CPU % across two
  samples; rate and ETA from a synthetic height ring; stalled detection.
- `internal/support`: redaction regexes on known inputs; crash-marker grep;
  archive contains the expected file names; peer IP redaction of a
  `networkInfo` document; log tail selection (newest 30, 4 MiB cap, gzip).
- `cmd/status --json` round-trips through `json.Unmarshal` into `Sample`.
- `top` view rendering at 80x24 and 120x40 via a golden-string test of the
  model's `View()` with a fixed sample (no real terminal needed).
- Anything that shells out to `systemctl`/`journalctl` uses the existing
  stub-on-PATH pattern from `internal/service`.

## README

New "Troubleshooting" section:

1. First look: `sudo nomctl status`, what each line means, healthy ranges.
2. Live: `sudo nomctl top`.
3. Common signatures and fixes: `too many open files` (unit has
   `LimitNOFILE=32768`; check with status), out of memory (`MemoryPeak`,
   kernel journal, 4 GiB minimum), disk full (backup dir vs data dir),
   `NotEnoughPeers` (firewall on 35995), sync done but frontier old (stalled;
   restart), crash loop (`NRestarts` climbing; run `support-bundle --watch`).
4. Support bundle: how to create one, what it contains, what it never
   contains, what to review before sharing, where to send it.

## Differences from the collect script

- Output directory defaults to `/root/...` instead of the current directory.
- Adds files 18, 19 and 20 (node RPC, nomctl state, status snapshot).
- Peer IPs in the RPC snapshot are redacted; the journal and process files
  are not scrubbed for IPs, exactly as before, and the closing warning says so.
- Watch samples are the structured `Sample` rather than raw `systemctl show`
  output, so they are comparable with `nomctl status`.
- `--service` and `--data` flags are replaced by `NOMCTL_SERVICE_NAME` and
  `NOMCTL_ZNN_DIR`.
