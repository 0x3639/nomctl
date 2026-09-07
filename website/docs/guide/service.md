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

These wrap `systemctl` for the `go-zenon` unit. `start` on an active service is a no-op. `stop` waits for systemd to finish stopping the unit and verifies its terminal state, including when a start or restart is pending. If systemd cannot report the unit's state, the command fails rather than guessing.

`start`, `stop`, `restart` and `logs` skip hardware, NTP and Internet pre-flight checks, so they remain available during an outage.

You do not need `start` after `deploy`: deploy enables and starts the service itself, and the unit is enabled, so it comes back after a reboot on its own. `start` is for bringing the node back after a `stop`.

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

Stops the node and verifies it has stopped, deletes `network`, `nom`, `consensus` and `log` under the data directory, and starts it again if it was running, starting or reloading. A node that was already stopping or stopped stays stopped. The wallet directory and `config.json` are kept. The command-line form does not ask for confirmation, matching the old non-interactive mode; the menu does.

## Locking

`start`, `stop`, `restart`, `backup`, `restore`, `bootstrap`, `resync` and `deploy` hold an exclusive lock on `/run/nomctl.lock` for their duration, whether started from the menu, the command line or the backup timer. A second operation that would overlap fails immediately with a message naming the running one.
