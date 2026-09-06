---
title: Service control and logs
description: "Start, stop, restart, follow logs, resync from genesis, and how nomctl locks node-data operations."
---

## start, stop, restart

```bash
sudo nomctl start
sudo nomctl stop
sudo nomctl restart
```

These wrap `systemctl` for the `go-zenon` unit. `start` on a running service and `stop` on a stopped one are no-ops with an informational message. If systemd cannot report the unit's state at all, the command fails rather than guessing.

## logs

```bash
sudo nomctl logs            # last 20 journal lines
sudo nomctl logs -n 200     # last 200
sudo nomctl logs -f         # follow; Ctrl+C stops the follower and returns
```

Following a stopped service prints the last lines with a warning instead.

## resync

```bash
sudo nomctl resync
```

Stops the node if running, deletes `network`, `nom`, `consensus` and `log` under the data directory, and starts it again if it was running. The wallet directory and `config.json` are kept. The command-line form does not ask for confirmation, matching the old non-interactive mode; the menu does.

## Locking

`backup`, `restore`, `resync` and `deploy` hold an exclusive lock on `/run/nomctl.lock` for their duration, whether started from the menu, the command line or the backup timer. A second operation that would overlap fails immediately with a message naming the running one.
