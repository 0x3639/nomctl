# nomctl

`nomctl` is a single static binary for deploying and operating [Zenon Network](https://zenon.network) (NoM) nodes on Debian/Ubuntu. It is a Go port of the bash toolkit at [hypercore-one/deployment](https://github.com/hypercore-one/deployment): the same interactive menu, the same non-interactive commands for automation, no dependency on `gum`, `jq` or any other helper.

**Documentation: [nomctl.0x3639.com](https://nomctl.0x3639.com)**

## Quick start

```bash
curl -fsSL https://raw.githubusercontent.com/0x3639/nomctl/main/install.sh | sudo bash
sudo nomctl deploy     # installs Go, builds znnd, creates and starts the go-zenon service
sudo nomctl status     # sync state, peers, pillar production, CPU, memory, disk
sudo nomctl            # the interactive menu
```

Then, optionally, `sudo nomctl alerts setup` for Telegram alerts (send `/start` to the bot first) and `sudo nomctl backup --schedule --cadence 7` for a backup timer.

Requirements: Debian or Ubuntu with systemd and apt (Ubuntu 24.04 is the tested target), `amd64` or `arm64`, root, 4 cores and 4 GiB RAM.

## What is where

- [Getting started](https://nomctl.0x3639.com/getting-started), the [command reference](https://nomctl.0x3639.com/reference/commands) and every [`NOMCTL_*` variable](https://nomctl.0x3639.com/reference/configuration)
- [Alerts](https://nomctl.0x3639.com/alerts/overview): pairing, rules, Telegram commands, [running your own relay](https://nomctl.0x3639.com/alerts/relay)
- [Troubleshooting](https://nomctl.0x3639.com/troubleshooting/first-look) and the [support bundle](https://nomctl.0x3639.com/troubleshooting/support-bundle)
- [Differences from the bash toolkit](https://nomctl.0x3639.com/reference/differences) and the [roadmap](https://nomctl.0x3639.com/roadmap)

## Building

```bash
make build      # ./bin/nomctl and ./bin/nomctl-relay for the host platform
make cross      # static linux/amd64 and linux/arm64 binaries in ./bin
make test
make lint       # gofmt + go vet + golangci-lint
```

Releases are produced by goreleaser on tag push: archives for both architectures, `checksums.txt` (which `install.sh` verifies) and the `ghcr.io/0x3639/nomctl-relay` image. The docs site lives in `website/` and deploys to GitHub Pages from `main`.

The module path is declared in `go.mod`; `make rename NEW=github.com/you/nomctl` rewrites it and every import. `install.sh` takes the GitHub repository slug from `NOMCTL_REPO` (default `0x3639/nomctl`). Source for the docs, design specs and plans is under `docs/` and `website/`.

## License

GPL-3.0, as a derivative of [hypercore-one/deployment](https://github.com/hypercore-one/deployment). See [LICENSE](LICENSE).
