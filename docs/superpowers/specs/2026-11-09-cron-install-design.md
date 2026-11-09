# Install missing cron when scheduling backups

## Problem and behavior

Scheduling currently fails on a fresh server without cron. Selecting **Schedule local backups**, running `nomctl backup schedule`, or using `nomctl backup --schedule` should install the missing scheduler on the supported Debian/Ubuntu hosts and finish setup in one operation.

Reuse an existing `cron.service` or `crond.service`. Only when both are absent, refresh apt's package lists and install `cron` without recommended packages. Log the installation step; use noninteractive apt and ask needrestart to list affected services rather than restart unrelated services. Do not broaden platform support or automatically install a different package manager.

After installation, reload systemd and require a known cron unit. Enable/start the unit and verify its active state before publishing the managed cron file or disabling the old nomctl systemd timer. Hold the existing operation lock throughout setup. Do not stop znnd or take a backup as part of scheduling.

## Failure handling

A systemd query error is not proof that cron is missing. Do not install in that case. Missing apt, apt failures, missing units after installation, and failed/inactive services return actionable errors without replacing the existing schedule or disabling the old timer. Leave a successfully installed package in place after a later failure so the operator can repair it and retry. Preserve existing timer-migration rollback on a cron-file publication failure.

## Acceptance checks

- Existing cron/crond requires no package installation; repeat scheduling writes one job.
- Missing cron is installed once and enabled; the unit and active state are checked after installation.
- Apt/update/install, systemd query/reload/start, and post-install detection failures preserve the old schedule.
- Unsupported package managers get manual installation guidance.
- A failed timer migration or final rename retains the existing recovery behavior.
- Tests stub every external command and cannot run the host's package manager; no path calls the node service.
