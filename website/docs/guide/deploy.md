---
title: Deploy
description: "How nomctl deploy builds znnd from source, installs Go, writes the go-zenon systemd unit, and how to choose a repository and branch."
---

`nomctl deploy` builds the node from source and sets up its systemd service.

```bash
sudo nomctl deploy [--repo URL] [--branch NAME]
```

## What happens

1. **Pre-flight checks**: at least 4 cores and 4 GiB RAM, `systemd-timesyncd` pointed at `time.cloudflare.com` (the config is patched and the service restarted if needed), and Internet connectivity. Every privileged command runs these; `--skip-preflight` or `NOMCTL_SKIP_PREFLIGHT=true` disables them.
2. **Dependencies**: `git`, `make` and `gcc` are installed with apt if missing.
3. **Go toolchain**: `NOMCTL_GO_VERSION` (default 1.23.0) is downloaded into `/opt/nomctl/go`. An existing toolchain of the same version is reused; a different version is moved aside.
4. **Clone and build**: the repository (`NOMCTL_REPO_URL`, default `https://github.com/zenon-network/go-zenon.git`) is cloned at `NOMCTL_BRANCH_NAME` (default `master`) into `/opt/nomctl/go-zenon`, an existing checkout is renamed with a timestamp, and `go build ./cmd/znnd` runs with the managed toolchain.
5. **Install**: the binary is copied to `/usr/local/bin/znnd`.
6. **Service**: `/etc/systemd/system/go-zenon.service` is written if absent, then enabled and started.

The unit that is written:

```ini
[Unit]
Description=znnd service
After=network.target
[Service]
LimitNOFILE=32768
User=root
Group=root
Type=simple
SuccessExitStatus=SIGKILL 9
ExecStart=/usr/local/bin/znnd
KillMode=control-group
Restart=on-failure
TimeoutStopSec=10s
TimeoutStartSec=10s
[Install]
WantedBy=multi-user.target
```

## Choosing a repository and branch

From the menu, deploy offers `zenon-network`, `hypercore-one` or a custom URL, then lists the remote branches with `master` first. From the command line pass `--repo` and `--branch`, or set `NOMCTL_REPO_URL` and `NOMCTL_BRANCH_NAME`.

Stopping sends SIGTERM to the service's own processes and SIGKILL after 10 seconds; nothing outside the unit's cgroup is touched, so several nodes can share a host. (The bash toolkit's unit ran `pkill -9 znnd`, which would have killed all of them.)

## Redeploying

Running `deploy` again rebuilds from the branch head and restarts the service. The previous checkout is kept as `/opt/nomctl/go-zenon-<timestamp>`. If the unit file on disk differs from the current definition, for example a unit written by an older nomctl, it is rewritten. Deploy takes the node-data lock, so it cannot overlap a backup, restore or resync.

## Node configuration

nomctl does not write `config.json`. znnd enables its HTTP RPC on `0.0.0.0:35997` by default, which is what `status`, `top` and alerts read from `127.0.0.1`.

That RPC has no authentication. Only 35995/TCP (peer-to-peer) needs to be reachable from the Internet; firewall 35997 and 35998 (WebSocket) unless you intend to serve RPC publicly, for example:

```bash
ufw allow 35995/tcp
ufw deny 35997/tcp
ufw deny 35998/tcp
```

Alternatively set `RPC.HTTPHost` to `127.0.0.1` in `config.json` and restart the node.
