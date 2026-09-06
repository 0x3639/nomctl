# Bootstrap sync

`nomctl bootstrap [URL]` replaces the node's chain data with a published
snapshot so a fresh node is in sync within minutes instead of days. It ports
the hypercore `restore-from-bootstrap` script.

## Archive format

The snapshot is produced by a separate service and hosted on DigitalOcean
Spaces; the operator makes it public when needed. nomctl accepts exactly that
format:

- a `.zip` whose entries live under `backup/nom.bak/`, `backup/network.bak/`
  and `backup/consensus.bak/`;
- a sidecar at the same URL with the extension replaced by `.hash`, holding
  the hex SHA-256 of the zip (first whitespace-separated token; a
  `sha256sum`-style line is accepted too).

The snapshot published for the release is the built-in default
(`config.DefaultBootstrapURL`); `NOMCTL_BOOTSTRAP_URL` overrides it and the
positional argument overrides both. Only `http` and `https` are accepted.

## Flow

1. Preflight: root, lock (`bootstrap`), URL present and well formed.
2. Download the hash sidecar, then the archive, into
   `<backup dir>/bootstrap/`. A HEAD request supplies the size; free space at
   the backup directory must cover it plus `NOMCTL_MIN_FREE_SPACE_KB`.
   Progress is logged (percentage and MB) about every five seconds, so it
   works under systemd and in a terminal alike. If the archive is already
   present and its hash verifies, the download is skipped, which makes a
   re-run after a failure cheap.
3. Verify SHA-256 against the sidecar before the node is touched.
4. Inspect the zip's central directory: each of the three directories must be
   present, entries outside `backup/<dir>.bak/` are rejected, and path
   traversal (`..`, absolute paths, symlinks) is rejected. The uncompressed
   total drives the second free-space check, at the data directory.
5. Swap:
   - default (keep): extract into a staging directory inside the data
     directory while the node keeps running, stop the service, move the
     current `nom`, `network` and `consensus` into
     `<backup dir>/restore/<dir>.bak.<unix>` the way `restore` does, rename
     the staged directories into place, start the service. If the move or
     the rename fails, the moved folders are put back and the service
     restarted, so the node keeps running on what it had;
   - `--discard`: check space counting what the delete will free, stop the
     service, delete the current directories, extract into staging, rename
     into place, start. There is nothing to roll back, so on failure the
     node stays stopped and re-running finishes the job.
   `cache` is not part of a snapshot and is never touched.
6. Only after the service has started are the archive and sidecar deleted;
   until then a verified download is reused by the next run. Print where the
   safety copy is and that it can be deleted once the node has synced.

The interactive menu gains "Restore Zenon from a bootstrap snapshot": it
prompts for the URL (prefilled from `NOMCTL_BOOTSTRAP_URL`), asks whether to
keep the previous data, shows a confirmation, then runs the same code. The
CLI does not confirm, matching `resync` and `restore`.

## Code

- `internal/bootstrap`: `HashURL`, `ParseHash`, `Inspect`, `Extract`,
  `Download` (context aware, progress callback), `Run(ctx, cfg, Options)`.
  Service start/stop go through package variables so `Run` is testable with
  an `httptest` server and a temp data directory.
- `internal/restore.MoveAside(cfg, stamp)` is factored out of `restore.Run`
  and shared.
- `cmd/bootstrap.go`, `config.BootstrapURL` (`NOMCTL_BOOTSTRAP_URL`, count 26),
  TUI entry, docs page `guide/bootstrap.md`, references, roadmap.

## Out of scope

Publishing snapshots from nomctl, accepting nomctl's own tar.gz backups from
a URL, resuming interrupted downloads, mirrors.
