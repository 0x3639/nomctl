---
title: Support bundle
description: "What nomctl support-bundle collects, what it never collects, and how --watch captures the moments before a restart."
---

```bash
sudo nomctl support-bundle                  # snapshot now
sudo nomctl support-bundle --watch          # wait for the next service restart, then snapshot
sudo nomctl support-bundle --since "2 days ago" --output /root/bundle-1
```

The command writes a directory such as `/root/nomctl-support-<host>-<time>` and a `.tar.gz` beside it, then prints both paths and the location of the crash-marker file. It is read-only: it never stops the node or touches its data, and diagnostic subprocesses have time and output limits.

The output directory and archive must not already exist. Each collection uses a new private directory; it never appends to an earlier bundle. The complete archive is published without replacing an existing file.

## What is in it

| File | Content |
|---|---|
| `02-summary.txt` | host, time, nomctl version, unit, data dir, journal window |
| `03`–`05` | `systemctl status`, unit properties and unit file, secrets redacted |
| `06`–`08` | service, kernel and system-warning journals for the window (default 12 hours), with up to 10,000 journal entries and 4 MiB per command |
| `09-live-process.txt`, `09-cgroup.txt` | `/proc` and cgroup details of the running process |
| `10-host-resources.txt` | memory, pressure, filesystems, inodes, top processes by RSS, limits |
| `11`, `12-app-log-tails/` | inventory of `<data dir>/log` and complete tail lines from the last 4 MiB of the 30 newest files, with credentials redacted |
| `13-crash-markers.log` | every line matching panic, fatal, oom, too many open files, corrupt, leveldb, killed, and similar, across the journals and log tails |
| `14`, `15` | coredump list and info |
| `16-binary.txt` | the running executable's path, size, sha256 and Go build info |
| `17-oom-and-boots.txt` | boot history, systemd-oomd |
| `18-node-rpc.json` | sync info, network info with peer IPs redacted, versions, frontier momentum |
| `19-nomctl.txt` | nomctl version, effective configuration with the password redacted, backup timer, log tail |
| `20-status.txt` | one `nomctl status` sample |
| `00`, `01` | with `--watch`: the live journal and one status sample per poll until the restart |

It never contains `config.json`, the wallet directory, or any file under the data directory other than `log/`. Known credential fields are redacted from captured output, including structured log records and authorization headers. Redaction is best effort: custom log formats can still include sensitive information. It may contain the host name, node addresses, file paths and peer counts; skim `13-crash-markers.log` and `06-service-journal.log` before sharing publicly. The directory is created with mode `0700` and the archive with `0600`.

## Watching for a crash

With `--watch`, nomctl samples the process every `--poll` (default 10 s) and follows the journal until it sees the unit restart (systemd's restart counter grows, or the main process disappears and a new one appears), `--timeout` elapses, or you press Ctrl+C, then collects the rest. Any restart ends the watch, whether it was a crash or a manual `nomctl restart`. Journal output is held in bounded memory and redacted before it is written to disk when watching ends. The journal and status watch each retain up to 4 MiB; after reaching the limit, monitoring continues but additional records are omitted and the file records that truncation occurred.
