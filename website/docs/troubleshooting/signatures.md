---
title: Common signatures
---

| What you see | Likely cause | What to do |
|---|---|---|
| `too many open files` in the journal | file descriptor limit reached | the unit sets `LimitNOFILE=32768`; check `open files` in `status`; if the unit was edited by hand, `sudo nomctl deploy` rewrites it |
| `out of memory`, `oom-kill`, `Main process exited, code=killed, status=9/KILL` in the kernel journal | host RAM exhausted | 4 GiB is the minimum; look at `MemoryPeak` in the bundle's `04-service-properties.txt` |
| `no space left on device` | data or backup filesystem full | `df -h`; prune backups with `nomctl backup --max-backups N`, or grow the disk |
| `not enough peers` for more than a few minutes | firewall | allow 35995/TCP inbound; confirm outbound Internet |
| `synced` but the frontier age keeps growing | stalled node | `sudo nomctl restart`; if it recurs, collect a bundle |
| service running, height not moving, sync state anything | node stopped syncing momentums | `sudo nomctl restart`; the `momentums_stalled` alert covers this |
| `leveldb` or `corrupt` errors after an unclean shutdown | damaged chain database | `sudo nomctl restore` from a backup, or `sudo nomctl resync` |
| restarts climbing, nothing obvious in the journal | crash loop | `sudo nomctl support-bundle --watch` and share the bundle |
| `node rpc unreachable ... context deadline exceeded` | RPC accepted the connection but did not answer in 3 seconds | the node is alive but overloaded (heavy initial sync, disk pressure) or wedged; check `top` for IO pressure, then `logs` |
| `node rpc unreachable ... connection refused` | nothing listening on 35997 | the process is down, or `config.json` disabled HTTP RPC |
