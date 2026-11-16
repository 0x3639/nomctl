# Backup status and schedule controls (R2)

Status: implemented, pending release. This extends the daily cron scheduler.

## Operator behavior

- `nomctl backup status [--json]` reports configured/paused/not-configured,
  scheduler service health, server timezone, next daily slot, saved destination
  and retention, latest scheduled attempt and success, and missed runs.
- `nomctl backup schedule` creates/replaces the schedule without a backup.
  `backup schedule edit --hour H --retention-days N --directory PATH` changes
  only supplied fields. Editing a paused schedule keeps it paused.
- `backup schedule pause` and `resume` are idempotent. Pause never deletes
  archives, stops znnd, or disables the system-wide cron daemon. Resume checks
  cron readiness before publishing the enabled job. No action runs a backup now.
- The Backups & recovery menu opens **Backup status & schedule**, with setup,
  edit, pause/resume, refresh and back actions. Protected restore points are
  explicitly separate from scheduled rotating backups.

## Schedule persistence and compatibility

Keep `/etc/cron.d/nomctl-backup` as the single authoritative schedule. Embed
versioned JSON settings in a comment and comment out the job when paused.
Atomically replace the complete file; validate the generated job against its
metadata before reading or editing it. Reject unexpected/manual changes rather
than silently claiming they are managed. Read the previous nomctl-generated
format with a restricted parser (never execute shell text), preserving captured
paths, retention and time when adopting it on edit/pause/resume. Legacy systemd
timers remain explicitly identified as legacy until setup migrates them.

All mutations reuse the shared operation lock and the existing cron-install and
timer-disable recovery path. Editing preserves executable, data/service/log
settings, free-space guard and minute unless explicitly supported by a control.
Changing the daily hour, retention or destination never prunes files immediately.

## Outcomes, concurrency and recovery

New cron entries invoke a tracked scheduled run. Re-read the schedule under the
operation lock so a queued invocation cannot bypass pause or use stale settings.
Persist start and completion records atomically under `/var/lib/nomctl/backups`,
independent of the archive filesystem. Report success, failure (with reason),
skipped-because-busy, and interrupted runs. Retain a bounded recent history and
the latest successful record. Manual backups and restore points are not counted
as successful scheduled runs. A record-write failure must be visible; failure
to record a start prevents node downtime. Preserve the existing backup space
checks, retention and restart-before-compression behavior.

Use server-local daily slots, with a short grace period before declaring an
unobserved slot missed. A past slot with no attempt since the schedule was
installed/resumed/edited is missed. Paused periods are not missed. History from
older installations is unknown, not fabricated from an archive's modification
time. The slot prediction follows Debian/Ubuntu cron’s fixed-time DST policy (run
soon after a skipped time, do not repeat after a backward jump). The daemon
controls actual dispatch; displayed times include the offset and timezone. There is no catch-up or
automatic retry after downtime, failure or lock contention; wait for the next
daily slot. Resume likewise never starts a backup immediately.

## Validation

Test legacy adoption and quoting, atomic failure preservation, repeated controls,
paused edits, saved-setting retention, no node/archive mutations during controls,
scheduled success/failure/busy/interruption, pause races, missed slots and timezone
transitions. Run the Go suite, static checks, Linux builds and documentation build
inside containers. Document the commands, menu, persistence paths and retry policy.
