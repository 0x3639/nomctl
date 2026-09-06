---
id: index
slug: /overview
title: What nomctl is
sidebar_label: Overview
---

nomctl is a single static binary for deploying and operating [Zenon Network](https://zenon.network) (NoM) nodes on Debian and Ubuntu. It is a Go port of the bash toolkit at [hypercore-one/deployment](https://github.com/hypercore-one/deployment): the same interactive menu and the same non-interactive commands for automation, with no dependency on `gum`, `jq` or any other helper.

## What it does

| Area | Commands |
|---|---|
| Deploy a node from source | `deploy` |
| Control the service | `start`, `stop`, `restart`, `logs`, `resync` |
| Back up and restore chain data | `backup`, `restore` |
| Watch the node | `status`, `top` |
| Get alerted on Telegram | `alerts` |
| Collect diagnostics to share | `support-bundle` |
| Install Prometheus and Grafana | `analytics install` |
| Do all of the above from a menu | `nomctl` with no arguments |

## Requirements

- Debian or Ubuntu with systemd and apt. The tested target is Ubuntu 24.04.
- `amd64` or `arm64`.
- root for every command except `--help`, `--version`, `env` and `completion`.
- At least 4 CPU cores and 4 GiB RAM, checked at startup.
- For the installer: `curl`, `tar` and `sha256sum`, all present on a stock Ubuntu install.

## Where things live

| Path | Purpose |
|---|---|
| `/usr/local/bin/nomctl` | the binary |
| `/usr/local/bin/znnd` | the node binary built by `deploy` |
| `/opt/nomctl` | Go toolchain and the go-zenon checkout |
| `/root/.znn` | node data directory (`NOMCTL_ZNN_DIR`) |
| `/backup` | backup archives (`NOMCTL_BACKUP_DIR`) |
| `/etc/nomctl/alerts.json` | alert pairing and thresholds |
| `/var/log/nomctl.log` | plain-text log of every run |
| `/etc/systemd/system/go-zenon.service` | the node unit |

Next: [Getting started](/getting-started).
