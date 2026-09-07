# Pillar producer setup

Port of the one thing znn_controller_dart's Deploy does that nomctl did
not: create a producer key store and wire it into `config.json` so the node
can produce momentums for a pillar.

## Key store

`internal/producer` creates and verifies key files with go-zenon's own
`wallet` package (imported cgo-free; only `wallet` and `common/types`
compile): 32 bytes of entropy, BIP39 mnemonic, SLIP-10 ed25519 derivation
at `m/44'/73404'/0'`, Argon2id + AES-256-GCM key file written with mode
0600 to `<data dir>/wallet/producer`. The file name is always `producer`,
as the controller and the rest of the ecosystem expect.

The password is 16 random characters from `[A-Za-z0-9]` (crypto/rand), or
`--password`. It is printed once at creation, as the controller does, and
stored in `config.json` in clear because znnd needs it to unlock the key.

## config.json

Read as `map[string]json.RawMessage` so every other section keeps its
content (re-indented on write); `Producer` is set to
`{"Index": 0, "KeyFilePath": "producer", "Password": ..., "Address": ...}`;
written with four-space indent and mode 0600 after copying the previous
file to `config.json.bak.<unix>`. A missing `config.json` is created with
only the `Producer` section; go-zenon fills defaults for the rest.

## `nomctl pillar setup [--password P] [--yes]`

1. Root, lock `pillar`.
2. If `config.json` already has a `Producer` whose key file exists: show
   the address and ask "keep the existing producer
   configuration?" (default yes); with `--yes` keep it. Nothing is
   rewritten in that case.
3. Else if `wallet/producer` exists: ask for its password (three attempts,
   hidden input; `--password` in scripts), verify by decrypting, configure
   with its address.
4. Else create a new key file with a generated (or given) password.
5. Write `config.json`, restart znnd if it is active (start is not forced:
   a node that is not deployed yet is left alone), print the address, the
   password when newly generated, and the instruction: set this address as
   the pillar's producer address (Syrius or znn-cli); one address serves
   one pillar; back up `wallet/producer` and the password.

## `nomctl pillar status [--show-password]`

Producer address, key file path and whether it exists, index, whether
znnd is running, and the pillar the address belongs to when the alerts
config names a pillar and the RPC answers (producer address compared with
`embedded.pillar.getByName`). The password is printed only with the flag.

## Menu and status

A "Pillar" entry opens a submenu: set up producer, show producer, back. The
`status` command adds a `Producer` line when one is configured.

## Out of scope

Registering the pillar or changing its producer address on chain (a wallet
action the owner performs in Syrius or znn-cli), key file names other than
`producer`, multiple producers, encrypted backups of the key (roadmap:
wallet and config backup).
