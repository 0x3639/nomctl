---
title: Pillar producer
description: "From a fresh server to a producing Pillar: install nomctl, run pillar deploy, set the producer address, sync, verify, back up the key."
---

A Pillar produces momentums through a node that holds its **producer key**. This page takes a fresh server to a producing Pillar, start to finish.

## From a fresh server to a producing Pillar

### 0. What you need

- A server running Ubuntu 24.04 with root access, at least 4 CPU cores and 4 GiB of RAM, and 60 GB of free disk to start with; the chain grows over time.
- Port 35995/TCP reachable from the Internet so other nodes can connect.
- Your Pillar's owner wallet in Syrius, or `znn-cli` with its key store, to set the producer address at the end. Registering a Pillar itself (the collateral transaction) is done there too and is outside nomctl.

### 1. Install nomctl

On the server, as root:

```bash
curl -fsSL https://raw.githubusercontent.com/0x3639/nomctl/main/install.sh | sudo bash
```

This downloads the release for your architecture, verifies its checksum and installs `/usr/local/bin/nomctl`. Nothing else happens yet.

### 2. Deploy the Pillar

```bash
sudo nomctl pillar deploy
```

Or run `sudo nomctl` and pick the first entry, **Deploy a Pillar**. Either way nomctl:

1. runs the pre-flight checks (cores, memory, time sync, connectivity);
2. installs `git`, `make` and `gcc` if missing and a Go toolchain under `/opt/nomctl/go`;
3. clones `github.com/zenon-network/go-zenon` and builds `znnd` from `master`, installs it to `/usr/local/bin`;
4. writes and enables the `go-zenon` systemd unit and starts it;
5. creates the producer key `/root/.znn/wallet/producer` with a generated password, writes the `Producer` section into `/root/.znn/config.json`, and restarts the node so it loads the key.

The build takes a few minutes. At the end it prints:

```text
Producer address   z1qp355um05ymgkzepnn9dra7twy995g7ndkfln6
Key file           /root/.znn/wallet/producer
Password           Fq8TQpKWLY9jX5p8
                   (also stored in config.json, which the node needs; keep a copy elsewhere)
```

**Copy the address and the password into your password manager now.** The password is shown once. It stays in `config.json` because znnd needs it, but that file is the only other place.

### 3. Point your Pillar at this node

In Syrius, open the Pillars tab, choose your Pillar, and set its **producer address** to the address printed above. With `znn-cli` the command is `pillar.updateProducer` on the owner's key store. One producer address serves one Pillar; never reuse it for a second one.

Once the transaction confirms, the network uses the new address for the Pillar's next momentum slots; `nomctl pillar status` reports when the chain shows this node's address.

### 4. Let the node sync

A new node syncs from genesis, which takes days. To finish in minutes instead:

```bash
sudo nomctl bootstrap
```

downloads a verified chain snapshot and swaps it in; see [Bootstrap](/guide/bootstrap). Then watch progress with `sudo nomctl status` or the live view `sudo nomctl top`. The Pillar can only produce once the node shows `synced`.

### 5. Verify

```bash
sudo nomctl pillar status
```

shows the producer address, that the key file is present and matches, that the node is running, and, once alerts are set up with the Pillar's name (next step), whether the Pillar on chain produces with this node's address.

### 6. Get alerts

```bash
sudo nomctl alerts setup --name <your pillar name>
```

after sending `/start` to the nomctl Telegram bot. Using the Pillar's name as the node name enables the `pillar_missed` alert, which tells you when the Pillar misses momentums. See [Alerts](/alerts/overview) and the privacy notice there.

### 7. Back up the key

`wallet/producer` and its password are what lets this node produce, and chain-data backups leave both out. Make the encrypted wallet backup and copy it off the machine:

```bash
sudo nomctl backup wallet
```

It asks for a passphrase and writes `<backup dir>/wallet/<service>_wallet_<date>.tar.gz.age`, which holds the key file and `config.json` and which the standard `age` tool can open anywhere. Details in [Backups](/guide/backups#wallet-and-config-backup). Enable `sudo nomctl alerts enable wallet_backup_missing` to be reminded if the key ever lacks a backup. If the key and its backup are both lost, run `sudo nomctl pillar setup` on a fresh node to create a new key and set the new address on the Pillar as in step 3.

## Already running a node?

The producer step alone is:

```bash
sudo nomctl pillar setup                 # create or configure, prompts on a terminal
sudo nomctl pillar setup --yes           # keep an existing configuration without asking
sudo nomctl pillar status                # address, key file, and which Pillar uses it
```

:::warning[The password unlocks the producer key]

On a terminal, let `setup` prompt: the answer is not echoed and never lands in shell history. In scripts pass it through the environment, `NOMCTL_PRODUCER_PASSWORD=... sudo -E nomctl pillar setup`, rather than `--password`, which shows up in shell history and process lists. `pillar status --show-password` prints it to the terminal; do not run that where output is logged.

:::

The menu has the same under **Pillar**.

## What the producer step does

1. If `config.json` already names a producer whose key file exists, it asks whether to keep it (kept without asking with `--yes`). Nothing is rewritten then.
2. Otherwise, if `wallet/producer` exists, it asks for that file's password (three attempts, hidden input; `NOMCTL_PRODUCER_PASSWORD` or `--password` in scripts), verifies it by opening the file, and configures the node with the address inside.
3. Otherwise it creates `wallet/producer` with a generated 16-character password, or the one you supply, and prints the password once.
4. It backs up `config.json` to `config.json.bak.<timestamp>`, writes the `Producer` section (`Index` 0, `KeyFilePath` `producer`, the password, the address), and restarts the node if it is running so it loads the key.

Then it prints the producer address. **Set that address as your Pillar's producer address** in Syrius or with `znn-cli`. One producer address serves one Pillar.

The key file is created with go-zenon's own wallet code, so it is exactly what the node would have written itself. The password is stored in `config.json` in clear because znnd needs it to unlock the key at start; that file is mode 0600.

## Status

`nomctl pillar status` shows the address, whether the key file is present and matches it, the index, whether the node is running, and, when the alerts configuration names a Pillar, whether that Pillar currently produces with this node's address. `nomctl status` adds a `Producer` line when one is configured.
