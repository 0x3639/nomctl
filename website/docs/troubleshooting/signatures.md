---
title: Common signatures
description: "Common failure signatures in the journal and status output, their likely cause, and what to do."
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
| `node rpc check failed ... context deadline exceeded` | the RPC check exceeded its timeout (3 seconds by default) | check service state, IO pressure in `top`, and `logs` |
| `node rpc check failed ... connection refused` | nothing accepted the connection at the displayed endpoint | check the process and `RPC.HTTPHost` / `RPC.HTTPPort` in `config.json`; restart the node if its listener settings changed |
| `HTTP 400: ... HTTP request to an HTTPS server` | plain HTTP reached a TLS listener, often Caddy on the public port | upgrade nomctl to a version that reads the internal HTTP endpoint from `config.json`; the public wallet endpoint can still work |
