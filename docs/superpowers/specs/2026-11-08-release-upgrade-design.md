# Signed node release upgrades

Add `nomctl upgrade-node --signed` and a main-menu action
for `zenon-network/go-zenon-releases`. Keep nomctl self-upgrades separate.
List published releases, including labelled prereleases, newest publication
first. The picker defaults to the first entry; `--version TAG` selects a
specific published release and `--yes` enables automation (latest by default).

Use GitHub's releases API and asset endpoints with optional
`NOMCTL_GITHUB_TOKEN` authentication. Public repositories work without a token.
Never log the token or forward it to download redirects or the public key URL.
Fetch `znnd-linux-{amd64,arm64}.tar.gz`, `SHA256CHECKSUMS.txt`, and its `.asc`.
Verify with GnuPG in an isolated keyring against the README's pinned primary
fingerprint `310A0EAEA8449754CF17E8BBF6B82D1155879DAB`; then verify the archive's
SHA-256 before extracting its single regular node binary. Reject unsupported
platforms, missing or duplicate assets, bad signatures and malformed archives.

Hold the existing node-operation lock throughout the operation. Echo each
step: download, verify signature, verify checksum, extract, prepare backup,
stop, copy chain data, install atomically, start and check service, finish the
backup archive and hash, apply retention. Reuse backup copy/archive/retention
code with a two-phase snapshot API; compression remains after service start.
Upgrade backups bypass scheduled cadence and exclude wallets like all existing
chain backups. Preserve the previous binary beside the configured target.

All downloads and validation precede downtime. Backup preparation checks space
before stopping. On copy/install failure, attempt to restart the original node
if previously active. On new-node startup failure, retain the new binary and
old binary, and finish the pre-upgrade backup for explicit operator recovery:
automatic binary rollback could run old code against migrated data. Archive
failure retains a unique snapshot directory, never cleared by a subsequent
backup. Report partial success accurately. No live node is modified in tests.

Tests cover API pagination/order/authentication, real GPG signature validation,
checksum and archive rejection, operation ordering, and failure recovery with
temporary data and stub systemctl commands. Build both Linux architectures.
