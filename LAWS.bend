import Base
import ./test/bend/model.bend as M

# Repository contract. Each law is linked to implementation evidence in
# test/bend/coverage.json. Proofs concern the model; see docs/BEND.md.

# Changes to node data or executable are gated before mutation. Requires locked.
law change_requires_locked:
  for +validated: Bool
  for +stopped: Bool
  for +recoverable: Bool
  {M.change(False{}, validated, stopped, recoverable) == False{} : Bool}

# Changes to node data or executable are gated before mutation. Requires validated.
law change_requires_validated:
  for +locked: Bool
  for +stopped: Bool
  for +recoverable: Bool
  {M.change(locked, False{}, stopped, recoverable) == False{} : Bool}

# Changes to node data or executable are gated before mutation. Requires stopped.
law change_requires_stopped:
  for +locked: Bool
  for +validated: Bool
  for +recoverable: Bool
  {M.change(locked, validated, False{}, recoverable) == False{} : Bool}

# Changes to node data or executable are gated before mutation. Requires recoverable.
law change_requires_recoverable:
  for +locked: Bool
  for +validated: Bool
  for +stopped: Bool
  {M.change(locked, validated, stopped, False{}) == False{} : Bool}

# Changes to node data or executable are gated before mutation. All prerequisites permit the modeled decision.
law change_accepts_valid:
  {M.change(True{}, True{}, True{}, True{}) == True{} : Bool}

# Recovery/probe output is published only with complete current evidence. Requires complete.
law publication_requires_complete:
  for +verified: Bool
  for +fresh: Bool
  for +owned: Bool
  {M.publish(False{}, verified, fresh, owned) == False{} : Bool}

# Recovery/probe output is published only with complete current evidence. Requires verified.
law publication_requires_verified:
  for +complete: Bool
  for +fresh: Bool
  for +owned: Bool
  {M.publish(complete, False{}, fresh, owned) == False{} : Bool}

# Recovery/probe output is published only with complete current evidence. Requires fresh.
law publication_requires_fresh:
  for +complete: Bool
  for +verified: Bool
  for +owned: Bool
  {M.publish(complete, verified, False{}, owned) == False{} : Bool}

# Recovery/probe output is published only with complete current evidence. Requires owned.
law publication_requires_owned:
  for +complete: Bool
  for +verified: Bool
  for +fresh: Bool
  {M.publish(complete, verified, fresh, False{}) == False{} : Bool}

# Recovery/probe output is published only with complete current evidence. All prerequisites permit the modeled decision.
law publication_accepts_valid:
  {M.publish(True{}, True{}, True{}, True{}) == True{} : Bool}

# Relay authentication requires known identity, signature and timestamp. Requires known.
law authentication_requires_known:
  for +signed: Bool
  for +fresh: Bool
  {M.authenticate(False{}, signed, fresh) == False{} : Bool}

# Relay authentication requires known identity, signature and timestamp. Requires signed.
law authentication_requires_signed:
  for +known: Bool
  for +fresh: Bool
  {M.authenticate(known, False{}, fresh) == False{} : Bool}

# Relay authentication requires known identity, signature and timestamp. Requires fresh.
law authentication_requires_fresh:
  for +known: Bool
  for +signed: Bool
  {M.authenticate(known, signed, False{}) == False{} : Bool}

# Relay authentication requires known identity, signature and timestamp. All prerequisites permit the modeled decision.
law authentication_accepts_valid:
  {M.authenticate(True{}, True{}, True{}) == True{} : Bool}

# Progress belongs to one identified, reachable process incarnation. Requires stable.
law progress_requires_stable:
  for +reachable: Bool
  for +identified: Bool
  for +increasing: Bool
  {M.progress(False{}, reachable, identified, increasing) == False{} : Bool}

# Progress belongs to one identified, reachable process incarnation. Requires reachable.
law progress_requires_reachable:
  for +stable: Bool
  for +identified: Bool
  for +increasing: Bool
  {M.progress(stable, False{}, identified, increasing) == False{} : Bool}

# Progress belongs to one identified, reachable process incarnation. Requires identified.
law progress_requires_identified:
  for +stable: Bool
  for +reachable: Bool
  for +increasing: Bool
  {M.progress(stable, reachable, False{}, increasing) == False{} : Bool}

# Progress belongs to one identified, reachable process incarnation. Requires increasing.
law progress_requires_increasing:
  for +stable: Bool
  for +reachable: Bool
  for +identified: Bool
  {M.progress(stable, reachable, identified, False{}) == False{} : Bool}

# Progress belongs to one identified, reachable process incarnation. All prerequisites permit the modeled decision.
law progress_accepts_valid:
  {M.progress(True{}, True{}, True{}, True{}) == True{} : Bool}

# Schedule proof readiness requires verified, current, continuous history. Requires history.
law replica_requires_history:
  for +current: Bool
  for +continuous: Bool
  for +complete: Bool
  {M.ready(False{}, current, continuous, complete) == False{} : Bool}

# Schedule proof readiness requires verified, current, continuous history. Requires current.
law replica_requires_current:
  for +history: Bool
  for +continuous: Bool
  for +complete: Bool
  {M.ready(history, False{}, continuous, complete) == False{} : Bool}

# Schedule proof readiness requires verified, current, continuous history. Requires continuous.
law replica_requires_continuous:
  for +history: Bool
  for +current: Bool
  for +complete: Bool
  {M.ready(history, current, False{}, complete) == False{} : Bool}

# Schedule proof readiness requires verified, current, continuous history. Requires complete.
law replica_requires_complete:
  for +history: Bool
  for +current: Bool
  for +continuous: Bool
  {M.ready(history, current, continuous, False{}) == False{} : Bool}

# Schedule proof readiness requires verified, current, continuous history. All prerequisites permit the modeled decision.
law replica_accepts_valid:
  {M.ready(True{}, True{}, True{}, True{}) == True{} : Bool}

# Failed alert delivery does not advance the last-success marker.
law failed_delivery_is_retryable:
  for +previous: Nat
  for +candidate: Nat
  {M.record_delivery(previous, candidate, False{}) == previous : Nat}

# Successful delivery records the candidate marker.
law successful_delivery_advances:
  for +previous: Nat
  for +candidate: Nat
  {M.record_delivery(previous, candidate, True{}) == candidate : Nat}

# Disabled alert rules never deliver.
law disabled_alerts_are_silent:
  for +changed: Bool
  for +due: Bool
  {M.deliver(False{}, changed, due) == False{} : Bool}

# A running, transitional or unknown service state cannot authorize destructive work.
law service_active_is_not_stopped:
  {M.stopped(M.Active{}) == False{} : Bool}

# A running, transitional or unknown service state cannot authorize destructive work.
law service_activating_is_not_stopped:
  {M.stopped(M.Activating{}) == False{} : Bool}

# A running, transitional or unknown service state cannot authorize destructive work.
law service_deactivating_is_not_stopped:
  {M.stopped(M.Deactivating{}) == False{} : Bool}

# A running, transitional or unknown service state cannot authorize destructive work.
law service_reloading_is_not_stopped:
  {M.stopped(M.Reloading{}) == False{} : Bool}

# A running, transitional or unknown service state cannot authorize destructive work.
law service_maintenance_is_not_stopped:
  {M.stopped(M.Maintenance{}) == False{} : Bool}

# A running, transitional or unknown service state cannot authorize destructive work.
law service_refreshing_is_not_stopped:
  {M.stopped(M.Refreshing{}) == False{} : Bool}

# A running, transitional or unknown service state cannot authorize destructive work.
law service_unknown_is_not_stopped:
  {M.stopped(M.Unknown{}) == False{} : Bool}

# Only terminal service states authorize the stopped predicate.
law service_terminal_states_are_stopped:
  {(M.stopped(M.Inactive{}) && (M.stopped(M.Failed{}) && M.stopped(M.NotFound{}))) == True{} : Bool}

# Chain-only operations exclude wallet data.
law backup_preserves_wallet:
  {M.backup_path(M.Wallet{}) == False{} : Bool}

# Chain-only operations exclude config data.
law backup_preserves_config:
  {M.backup_path(M.Config{}) == False{} : Bool}

# Chain-only operations exclude other data.
law backup_preserves_other:
  {M.backup_path(M.Other{}) == False{} : Bool}

# Chain-only operations exclude wallet data.
law resync_preserves_wallet:
  {M.resync_path(M.Wallet{}) == False{} : Bool}

# Chain-only operations exclude config data.
law resync_preserves_config:
  {M.resync_path(M.Config{}) == False{} : Bool}

# Chain-only operations exclude other data.
law resync_preserves_other:
  {M.resync_path(M.Other{}) == False{} : Bool}

# Archive extraction requires containment, safe entry type and allowed scope. Requires contained.
law archive_requires_contained:
  for +safe_type: Bool
  for +allowed: Bool
  {M.archive_entry(False{}, safe_type, allowed) == False{} : Bool}

# Archive extraction requires containment, safe entry type and allowed scope. Requires safe_type.
law archive_requires_safe_type:
  for +contained: Bool
  for +allowed: Bool
  {M.archive_entry(contained, False{}, allowed) == False{} : Bool}

# Archive extraction requires containment, safe entry type and allowed scope. Requires allowed.
law archive_requires_allowed:
  for +contained: Bool
  for +safe_type: Bool
  {M.archive_entry(contained, safe_type, False{}) == False{} : Bool}

# Archive extraction requires containment, safe entry type and allowed scope. All prerequisites permit the modeled decision.
law archive_accepts_valid:
  {M.archive_entry(True{}, True{}, True{}) == True{} : Bool}

# Keeping every archive prunes none.
law pruning_keeps_requested_count:
  for +count: Nat
  {M.prune_count(count, count) == 0n : Nat}

# Zero retention count prunes every ordinary archive.
law pruning_zero_keeps_nothing:
  for +count: Nat
  {M.prune_count(count, 0n) == count : Nat}

# An empty archive set remains empty for any retention count.
law pruning_empty_is_empty:
  for +keep: Nat
  {M.prune_count(0n, keep) == 0n : Nat}

# A zero resource budget admits no output.
law bounded_output_zero_budget:
  for +size: Nat
  {M.minimum(size, 0n) == 0n : Nat}

# An empty input consumes no budget.
law bounded_output_empty:
  for +budget: Nat
  {M.minimum(0n, budget) == 0n : Nat}

# A precisely fitting input is retained.
law bounded_output_exact_budget:
  for +size: Nat
  {M.minimum(size, size) == size : Nat}

# No later pause, resume, edit or tick revives an already captured stale invocation.
law stale_schedule_never_recovers:
  for +events: +List<M.ScheduleEvent>
  for +enabled: Bool
  for +locked: Bool
  {M.scheduled(M.schedule_run(events, M.Schedule{enabled, False{}}), locked) == False{} : Bool}

# Paused schedules cannot run even with the current generation.
law paused_schedule_cannot_run:
  for +fresh: Bool
  for +locked: Bool
  {M.scheduled(M.Schedule{False{}, fresh}, locked) == False{} : Bool}

# Scheduled mutation requires exclusive ownership.
law schedule_requires_lock:
  for +enabled: Bool
  for +fresh: Bool
  {M.scheduled(M.Schedule{enabled, fresh}, False{}) == False{} : Bool}

# Each migration action preserves the single-active-node invariant.
law handoff_preserves_exclusion:
  for +s: M.Handoff
  for +e: M.HandoffEvent
  for safe: {M.dual_active(s) == False{} : Bool}
  {M.dual_active(M.handoff_step(s, e)) == False{} : Bool}

# Every finite modeled handoff history preserves exclusion from a safe starting state.
law migration_never_runs_both_nodes:
  for +events: +List<M.HandoffEvent>
  for +s: M.Handoff
  for safe: {M.dual_active(s) == False{} : Bool}
  {M.dual_active(M.handoff_run(events, s)) == False{} : Bool}

# Missing, failed or stalled health evidence cannot report success.
law health_stalled_does_not_pass:
  {M.passed(M.Stalled{}) == False{} : Bool}

# Missing, failed or stalled health evidence cannot report success.
law health_uncertain_does_not_pass:
  {M.passed(M.Uncertain{}) == False{} : Bool}

# Missing, failed or stalled health evidence cannot report success.
law health_unhealthy_does_not_pass:
  {M.passed(M.Unhealthy{}) == False{} : Bool}

# Healthy and syncing reports both acknowledge valid progress.
law health_progress_can_pass:
  {(M.passed(M.Healthy{}) && M.passed(M.Syncing{})) == True{} : Bool}

# An explicit configured value overrides its fallback.
law configured_value_wins:
  for +value: Nat
  for +fallback: Nat
  {M.effective(Some{value}, fallback) == value : Nat}

# Absent configuration uses the documented default.
law absent_value_uses_default:
  for +fallback: Nat
  {M.effective(None{}, fallback) == fallback : Nat}

# CLI flags override environment configuration.
law flags_override_environment:
  for +flag: Nat
  for +env: Maybe<&2, Nat>
  for +fallback: Nat
  {M.precedence(Some{flag}, env, fallback) == flag : Nat}

# Invalid configuration cannot replace the previous configuration.
law invalid_config_preserves_old:
  for +old: Nat
  for +new: Nat
  {M.save(old, new, False{}) == old : Nat}

# Failed atomic installation retains the previous value.
law failed_commit_preserves_old:
  for +old: Nat
  for +staged: Nat
  {M.commit(old, staged, False{}) == old : Nat}

# A successful atomic installation publishes the staged value.
law successful_commit_installs_new:
  for +old: Nat
  for +staged: Nat
  {M.commit(old, staged, True{}) == staged : Nat}

# Classified secret values are never exposed after redaction.
law secrets_are_redacted:
  for +secret: Nat
  {M.redact(M.Secret{secret}) == M.Redacted{} : M.Text}

# Redaction preserves classified public data.
law public_text_is_preserved:
  for +value: Nat
  {M.redact(M.Public{value}) == M.Public{value} : M.Text}

# Redacting a second time cannot reintroduce a secret.
law redaction_is_idempotent:
  for +text: M.Text
  {M.redact(M.redact(text)) == M.redact(text) : M.Text}

# Unknown alert names are rejected for every state and severity.
law unknown_alert_is_rejected:
  for +state: M.AlertState
  for +severity: M.Severity
  for +bounded: Bool
  {M.valid_alert(M.Unrecognized{}, state, severity, bounded) == False{} : Bool}

# Informational events cannot become firing alerts.
law events_cannot_fire:
  for +severity: M.Severity
  for +bounded: Bool
  {M.valid_alert(M.Event{}, M.Firing{}, severity, bounded) == False{} : Bool}

# No valid alert classification bypasses the text budget.
law oversized_alert_is_rejected:
  for +kind: M.AlertKind
  for +state: M.AlertState
  for +severity: M.Severity
  {M.valid_alert(kind, state, severity, False{}) == False{} : Bool}

# One UI step invokes its operation once.
law ui_runs_action_once:
  for +calls: Nat
  {M.step_count(calls) == 1n+calls : Nat}
