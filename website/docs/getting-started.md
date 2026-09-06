---
title: Getting started
---

Three commands take a fresh Ubuntu 24.04 server to a syncing node.

## 1. Install nomctl

```bash
curl -fsSL https://raw.githubusercontent.com/0x3639/nomctl/main/install.sh | sudo bash
```

The installer detects the architecture, downloads the latest GitHub release, verifies its sha256 against `checksums.txt` and installs the binary to `/usr/local/bin`. It is a short script; read it before piping it to a root shell, or download it first:

```bash
curl -fsSLO https://raw.githubusercontent.com/0x3639/nomctl/main/install.sh
less install.sh
sudo bash install.sh
```

To pin a version instead of `main`, fetch the script from the release tag and tell it which release to install:

```bash
curl -fsSL https://raw.githubusercontent.com/0x3639/nomctl/v0.4.0/install.sh | sudo NOMCTL_VERSION=v0.4.0 bash
``` It honours `NOMCTL_VERSION` (a release tag, default `latest`), `NOMCTL_INSTALL_DIR` and `NOMCTL_REPO`. Rerunning it upgrades in place.

To build from source instead, with Go 1.24 or newer:

```bash
go install github.com/0x3639/nomctl@latest
```

## 2. Deploy the node

```bash
sudo nomctl deploy
```

This installs `git`, `make` and `gcc` if missing, downloads the Go toolchain into `/opt/nomctl/go`, clones go-zenon, builds `znnd`, installs it to `/usr/local/bin`, writes and enables the `go-zenon` systemd unit and starts it. Details in [Deploy](/guide/deploy).

## 3. Watch it sync

```bash
sudo nomctl status
sudo nomctl top
sudo nomctl logs -f
```

`status` prints one screen of service, sync, process and host state; `top` refreshes it live; `logs -f` follows the journal. See [First look](/troubleshooting/first-look) for how to read the numbers.

## 4. Optional but recommended

- **Alerts**: send `/start` to the nomctl Telegram bot, then `sudo nomctl alerts setup`. Five minutes, no bot to create. See [Alerts](/alerts/overview).
- **Backups**: `sudo nomctl backup --schedule --cadence 7` installs a timer. See [Backups](/guide/backups).

## The menu

Everything above is also in the interactive menu:

```bash
sudo nomctl
```

Every menu action is a subcommand with the same behaviour, so what you learn in the menu carries over to scripts.
