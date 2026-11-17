# R3: Verify backups and restore points

Add **Backups & recovery → Verify recovery files** and `nomctl verify`.
The default checks all ordinary backups and protected restore points for the
configured service and backup directory. `--file` and `--point` select one;
`--json` provides structured results. Incomplete archives and damaged points
remain visible and fail clearly. Older point metadata without version/commit
is accepted when its required checksum and completion fields are valid.

Verification holds the shared operation lock, reads files without starting or
stopping znnd, checks the archive checksum and every archive entry, and checks
the saved executable checksum for a full point. It never executes that binary,
changes live data, prunes archives, or deletes a failed point. Readers reject
links and special files and support cancellation. Archive inspection consumes
the gzip trailer and rejects truncation, invalid paths, duplicate/conflicting
entries, links, and unexpected trailing content. Restore uses the same checks.

Results include start/end time, checks and failures, SHA-256 content identities,
and file identity (device, inode, size, mode, modification/change times) for
the archive, sidecar, metadata, and saved binary. Open descriptors and paths are
rechecked before publishing success. Persist the latest result per artifact
atomically under `/var/lib/nomctl/verification`; recording failure is itself an
error. Status reports stale if any component changed or disappeared, even when
mtime and size are restored. An unfinished attempt is never a success. Saved
status rechecks compressed-file and binary content hashes as well as metadata;
coarse filesystem timestamps alone are insufficient. Archive inspection and
rehearsal keep their original timestamps. Content checks support cancellation and run on request in the menu, not on
every menu visit.

An opt-in extraction rehearsal (`--rehearse`) uses a private temporary directory
under the backup directory, checks apparent expanded size plus metadata overhead
against the existing disk/free-space policy before writing, and rechecks space
during extraction. Cleanup runs on success, error, and cancellation. No service
or executable is started. Report rehearsal separately from integrity: neither
establishes database-version compatibility or guarantees successful startup.

`verify schedule --hour H [--directory PATH]` installs a separate daily cron job; pause/resume and
status are available in the menu and CLI. Store the exact directory, service,
executable, and log settings in the managed file. Setup validates settings and
cron readiness before atomic publication; edits preserve pause state. Scheduled
verification always checks all artifacts and never rehearses extraction. It
checks the schedule generation after taking the shared lock. Busy runs are
recorded and skipped, with no catch-up or automatic retry. The next daily slot
is the next attempt. The backup schedule and retention are unchanged.

Validation covers corrupt/truncated archives including gzip trailers, hostile
paths/types and duplicate entries, bad/missing checksums and components, legacy
points, replaced/modified artifacts, concurrent changes, cancellation, rehearsal
space/cleanup, schedule quoting/pause/races, lock contention, CLI and menu wiring,
and unchanged live data/services. Run all tests, lint, cross-builds, and docs.
