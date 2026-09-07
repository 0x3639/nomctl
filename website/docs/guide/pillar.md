---
title: Pillar producer
description: "Turn a node into a Pillar's producer: nomctl pillar setup creates the producer key, wires it into config.json and prints the address to register."
---

A Pillar produces momentums through a node that holds its **producer key**. `nomctl pillar setup` gives this node one, the way znn-controller's Deploy did:

```bash
sudo nomctl pillar setup                 # create or configure, prompts on a terminal
sudo nomctl pillar setup --yes           # keep an existing configuration without asking
sudo nomctl pillar status                # address, key file, and which Pillar uses it
```

:::warning[The password unlocks the producer key]

On a terminal, let `setup` prompt: the answer is not echoed and never lands in shell history. In scripts pass it through the environment, `NOMCTL_PRODUCER_PASSWORD=... sudo -E nomctl pillar setup`, rather than `--password`, which shows up in shell history and process lists. `pillar status --show-password` prints it to the terminal; do not run that where output is logged.

:::

The menu has the same under **Pillar**.

## What setup does

1. If `config.json` already names a producer whose key file exists, it asks whether to keep it (kept without asking with `--yes`). Nothing is rewritten then.
2. Otherwise, if `wallet/producer` exists, it asks for that file's password (three attempts, hidden input; `NOMCTL_PRODUCER_PASSWORD` or `--password` in scripts), verifies it by opening the file, and configures the node with the address inside.
3. Otherwise it creates `wallet/producer` with a generated 16-character password, or the one you supply, and prints the password once.
4. It backs up `config.json` to `config.json.bak.<timestamp>`, writes the `Producer` section (`Index` 0, `KeyFilePath` `producer`, the password, the address), and restarts the node if it is running so it loads the key.

Then it prints the producer address. **Set that address as your Pillar's producer address** in Syrius or with `znn-cli`. One producer address serves one Pillar.

The key file is created with go-zenon's own wallet code, so it is exactly what the node would have written itself. The password is stored in `config.json` in clear because znnd needs it to unlock the key at start; that file is mode 0600.

## Back it up

`wallet/producer` and its password are what lets this node produce. Without them, create a new key with `pillar setup` on a fresh node and update the Pillar's producer address. Chain-data backups (`nomctl backup`) do not include the wallet directory.

## Status

`nomctl pillar status` shows the address, whether the key file is present and matches it, the index, whether the node is running, and, when the alerts configuration names a Pillar, whether that Pillar currently produces with this node's address. `nomctl status` adds a `Producer` line when one is configured.
