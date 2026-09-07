---
title: config.json editor
description: "Show, get, set, unset and edit the node's config.json with validation against go-zenon's schema, backups of every previous file, and an optional restart."
---

`config.json` in the data directory is where znnd reads its settings. nomctl edits it with validation, so a mistyped key or an out-of-range value is refused instead of being silently ignored by the node.

```bash
sudo nomctl config show                       # every setting, its value, and whether it is in the file or a default
sudo nomctl config get RPC.HTTPHost
sudo nomctl config set RPC.HTTPHost 127.0.0.1 # typed and validated, previous file backed up
sudo nomctl config set Net.MaxPeers 80 --restart
sudo nomctl config set Net.Seeders "enode://a,enode://b"
sudo nomctl config unset Net.MaxPeers         # back to go-zenon's default
sudo nomctl config edit                       # $EDITOR, validated before it is installed
```

The menu has the same under **Edit config.json**: show, set one setting with a picker, edit in the editor. There it asks whether to restart the node after a change; on the command line pass `--restart`, otherwise `sudo nomctl restart` when you are done, since znnd reads the file only at start.

## Keys

Keys are dotted, as in go-zenon's config struct:

| Key | Type | Default | |
|---|---|---|---|
| `Name` | string | `znn-node` | node name shown to peers |
| `LogLevel` | string | `info` | `debug`, `info`, `warn`, `error` or `crit` |
| `RPC.EnableHTTP`, `RPC.EnableWS` | bool | `true` | serve JSON-RPC over HTTP / WebSocket |
| `RPC.HTTPHost`, `RPC.WSHost` | string | `0.0.0.0` | bind addresses; `127.0.0.1` keeps them local |
| `RPC.HTTPPort`, `RPC.WSPort` | int | `35997`, `35998` | ports |
| `RPC.Endpoints` | list | empty | extra API namespaces |
| `RPC.HTTPVirtualHosts`, `RPC.HTTPCors`, `RPC.WSOrigins` | list | see `show` | HTTP access lists |
| `Net.ListenHost`, `Net.ListenPort` | string, int | `0.0.0.0`, `35995` | P2P bind address and port |
| `Net.MinPeers`, `Net.MinConnectedPeers`, `Net.MaxPeers`, `Net.MaxPendingPeers` | int | `8`, `16`, `60`, `10` | peer limits |
| `Net.Seeders` | list | empty | seed nodes; empty uses go-zenon's list |
| `DataPath`, `WalletPath`, `GenesisFile` | string | unset | paths; leave unset unless you know why |
| `Producer.*` | | | managed by [`nomctl pillar setup`](/guide/pillar); `set` refuses them |

Lists take a comma-separated string or a JSON array. Booleans take `true`/`false`, `yes`/`no`, `on`/`off`.

## Validation

Before anything is written: ports are within 1–65535, peer counts are not negative and `MaxPeers` is not below `MinPeers`, `LogLevel` is one of go-zenon's levels, bind addresses are not empty, every value has the right type, and each section is an object. `set` refuses an unknown key by default; `--force` writes it as text, unchecked, for a key nomctl does not know yet. `edit` refuses a file with unknown keys, since a typo there is exactly what it guards against. Neither `set`, `unset` nor `edit` changes the `Producer` section, which belongs to `pillar setup`. `show` lists any unknown keys already in the file.

## Backups and safety

Every write copies the previous file to `config.json.bak.<timestamp>` first (a suffix is added if two writes land in the same second), then replaces the file atomically with mode 0600 because it holds the producer password. `edit` works on a copy, the way `visudo` does: the original is untouched until the copy validates, and on a problem you can reopen the editor with the problems shown or discard the edit. Sections nomctl does not know are written back with their content unchanged.
