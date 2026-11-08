# Release verification fixtures

`SHA256CHECKSUMS.txt` and its detached signature are from
https://github.com/zenon-network/go-zenon-releases/releases/tag/v0.0.10.
`signer.asc` is the public key downloaded from https://github.com/0x3639.gpg.
Captured on 2026-11-08. No secret key or release executable is included.

Tests use GnuPG's real verification with the production pinned fingerprint.
The key is a fixture only; runtime upgrades fetch the current profile key.
