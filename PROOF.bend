import Base
import ./test/bend/model.bend as M
import ./LAWS.bend as Laws

def Laws.change_requires_locked(validated, stopped, recoverable):
  match validated stopped recoverable:
    case False{} False{} False{} :
      {==}
    case False{} False{} True{} :
      {==}
    case False{} True{} False{} :
      {==}
    case False{} True{} True{} :
      {==}
    case True{} False{} False{} :
      {==}
    case True{} False{} True{} :
      {==}
    case True{} True{} False{} :
      {==}
    case True{} True{} True{} :
      {==}

def Laws.change_requires_validated(locked, stopped, recoverable):
  match locked stopped recoverable:
    case False{} False{} False{} :
      {==}
    case False{} False{} True{} :
      {==}
    case False{} True{} False{} :
      {==}
    case False{} True{} True{} :
      {==}
    case True{} False{} False{} :
      {==}
    case True{} False{} True{} :
      {==}
    case True{} True{} False{} :
      {==}
    case True{} True{} True{} :
      {==}

def Laws.change_requires_stopped(locked, validated, recoverable):
  match locked validated recoverable:
    case False{} False{} False{} :
      {==}
    case False{} False{} True{} :
      {==}
    case False{} True{} False{} :
      {==}
    case False{} True{} True{} :
      {==}
    case True{} False{} False{} :
      {==}
    case True{} False{} True{} :
      {==}
    case True{} True{} False{} :
      {==}
    case True{} True{} True{} :
      {==}

def Laws.change_requires_recoverable(locked, validated, stopped):
  match locked validated stopped:
    case False{} False{} False{} :
      {==}
    case False{} False{} True{} :
      {==}
    case False{} True{} False{} :
      {==}
    case False{} True{} True{} :
      {==}
    case True{} False{} False{} :
      {==}
    case True{} False{} True{} :
      {==}
    case True{} True{} False{} :
      {==}
    case True{} True{} True{} :
      {==}

def Laws.change_accepts_valid():
  {==}

def Laws.publication_requires_complete(verified, fresh, owned):
  match verified fresh owned:
    case False{} False{} False{} :
      {==}
    case False{} False{} True{} :
      {==}
    case False{} True{} False{} :
      {==}
    case False{} True{} True{} :
      {==}
    case True{} False{} False{} :
      {==}
    case True{} False{} True{} :
      {==}
    case True{} True{} False{} :
      {==}
    case True{} True{} True{} :
      {==}

def Laws.publication_requires_verified(complete, fresh, owned):
  match complete fresh owned:
    case False{} False{} False{} :
      {==}
    case False{} False{} True{} :
      {==}
    case False{} True{} False{} :
      {==}
    case False{} True{} True{} :
      {==}
    case True{} False{} False{} :
      {==}
    case True{} False{} True{} :
      {==}
    case True{} True{} False{} :
      {==}
    case True{} True{} True{} :
      {==}

def Laws.publication_requires_fresh(complete, verified, owned):
  match complete verified owned:
    case False{} False{} False{} :
      {==}
    case False{} False{} True{} :
      {==}
    case False{} True{} False{} :
      {==}
    case False{} True{} True{} :
      {==}
    case True{} False{} False{} :
      {==}
    case True{} False{} True{} :
      {==}
    case True{} True{} False{} :
      {==}
    case True{} True{} True{} :
      {==}

def Laws.publication_requires_owned(complete, verified, fresh):
  match complete verified fresh:
    case False{} False{} False{} :
      {==}
    case False{} False{} True{} :
      {==}
    case False{} True{} False{} :
      {==}
    case False{} True{} True{} :
      {==}
    case True{} False{} False{} :
      {==}
    case True{} False{} True{} :
      {==}
    case True{} True{} False{} :
      {==}
    case True{} True{} True{} :
      {==}

def Laws.publication_accepts_valid():
  {==}

def Laws.authentication_requires_known(signed, fresh):
  match signed fresh:
    case False{} False{} :
      {==}
    case False{} True{} :
      {==}
    case True{} False{} :
      {==}
    case True{} True{} :
      {==}

def Laws.authentication_requires_signed(known, fresh):
  match known fresh:
    case False{} False{} :
      {==}
    case False{} True{} :
      {==}
    case True{} False{} :
      {==}
    case True{} True{} :
      {==}

def Laws.authentication_requires_fresh(known, signed):
  match known signed:
    case False{} False{} :
      {==}
    case False{} True{} :
      {==}
    case True{} False{} :
      {==}
    case True{} True{} :
      {==}

def Laws.authentication_accepts_valid():
  {==}

def Laws.progress_requires_stable(reachable, identified, increasing):
  match reachable identified increasing:
    case False{} False{} False{} :
      {==}
    case False{} False{} True{} :
      {==}
    case False{} True{} False{} :
      {==}
    case False{} True{} True{} :
      {==}
    case True{} False{} False{} :
      {==}
    case True{} False{} True{} :
      {==}
    case True{} True{} False{} :
      {==}
    case True{} True{} True{} :
      {==}

def Laws.progress_requires_reachable(stable, identified, increasing):
  match stable identified increasing:
    case False{} False{} False{} :
      {==}
    case False{} False{} True{} :
      {==}
    case False{} True{} False{} :
      {==}
    case False{} True{} True{} :
      {==}
    case True{} False{} False{} :
      {==}
    case True{} False{} True{} :
      {==}
    case True{} True{} False{} :
      {==}
    case True{} True{} True{} :
      {==}

def Laws.progress_requires_identified(stable, reachable, increasing):
  match stable reachable increasing:
    case False{} False{} False{} :
      {==}
    case False{} False{} True{} :
      {==}
    case False{} True{} False{} :
      {==}
    case False{} True{} True{} :
      {==}
    case True{} False{} False{} :
      {==}
    case True{} False{} True{} :
      {==}
    case True{} True{} False{} :
      {==}
    case True{} True{} True{} :
      {==}

def Laws.progress_requires_increasing(stable, reachable, identified):
  match stable reachable identified:
    case False{} False{} False{} :
      {==}
    case False{} False{} True{} :
      {==}
    case False{} True{} False{} :
      {==}
    case False{} True{} True{} :
      {==}
    case True{} False{} False{} :
      {==}
    case True{} False{} True{} :
      {==}
    case True{} True{} False{} :
      {==}
    case True{} True{} True{} :
      {==}

def Laws.progress_accepts_valid():
  {==}

def Laws.replica_requires_history(current, continuous, complete):
  match current continuous complete:
    case False{} False{} False{} :
      {==}
    case False{} False{} True{} :
      {==}
    case False{} True{} False{} :
      {==}
    case False{} True{} True{} :
      {==}
    case True{} False{} False{} :
      {==}
    case True{} False{} True{} :
      {==}
    case True{} True{} False{} :
      {==}
    case True{} True{} True{} :
      {==}

def Laws.replica_requires_current(history, continuous, complete):
  match history continuous complete:
    case False{} False{} False{} :
      {==}
    case False{} False{} True{} :
      {==}
    case False{} True{} False{} :
      {==}
    case False{} True{} True{} :
      {==}
    case True{} False{} False{} :
      {==}
    case True{} False{} True{} :
      {==}
    case True{} True{} False{} :
      {==}
    case True{} True{} True{} :
      {==}

def Laws.replica_requires_continuous(history, current, complete):
  match history current complete:
    case False{} False{} False{} :
      {==}
    case False{} False{} True{} :
      {==}
    case False{} True{} False{} :
      {==}
    case False{} True{} True{} :
      {==}
    case True{} False{} False{} :
      {==}
    case True{} False{} True{} :
      {==}
    case True{} True{} False{} :
      {==}
    case True{} True{} True{} :
      {==}

def Laws.replica_requires_complete(history, current, continuous):
  match history current continuous:
    case False{} False{} False{} :
      {==}
    case False{} False{} True{} :
      {==}
    case False{} True{} False{} :
      {==}
    case False{} True{} True{} :
      {==}
    case True{} False{} False{} :
      {==}
    case True{} False{} True{} :
      {==}
    case True{} True{} False{} :
      {==}
    case True{} True{} True{} :
      {==}

def Laws.replica_accepts_valid():
  {==}

def Laws.failed_delivery_is_retryable(previous, candidate):
  {==}

def Laws.successful_delivery_advances(previous, candidate):
  {==}

def Laws.disabled_alerts_are_silent(changed, due):
  {==}

def Laws.service_active_is_not_stopped():
  {==}

def Laws.service_activating_is_not_stopped():
  {==}

def Laws.service_deactivating_is_not_stopped():
  {==}

def Laws.service_reloading_is_not_stopped():
  {==}

def Laws.service_maintenance_is_not_stopped():
  {==}

def Laws.service_refreshing_is_not_stopped():
  {==}

def Laws.service_unknown_is_not_stopped():
  {==}

def Laws.service_terminal_states_are_stopped():
  {==}

def Laws.backup_preserves_wallet():
  {==}

def Laws.backup_preserves_config():
  {==}

def Laws.backup_preserves_other():
  {==}

def Laws.resync_preserves_wallet():
  {==}

def Laws.resync_preserves_config():
  {==}

def Laws.resync_preserves_other():
  {==}

def Laws.archive_requires_contained(safe_type, allowed):
  match safe_type allowed:
    case False{} False{} :
      {==}
    case False{} True{} :
      {==}
    case True{} False{} :
      {==}
    case True{} True{} :
      {==}

def Laws.archive_requires_safe_type(contained, allowed):
  match contained allowed:
    case False{} False{} :
      {==}
    case False{} True{} :
      {==}
    case True{} False{} :
      {==}
    case True{} True{} :
      {==}

def Laws.archive_requires_allowed(contained, safe_type):
  match contained safe_type:
    case False{} False{} :
      {==}
    case False{} True{} :
      {==}
    case True{} False{} :
      {==}
    case True{} True{} :
      {==}

def Laws.archive_accepts_valid():
  {==}

def Laws.pruning_keeps_requested_count(count):
  match count:
    case 0n: {==}
    case 1n+n: Laws.pruning_keeps_requested_count(n)

def Laws.pruning_zero_keeps_nothing(count):
  match count:
    case 0n: {==}
    case 1n+n: {==}

def Laws.pruning_empty_is_empty(keep):
  {==}

def Laws.bounded_output_zero_budget(size):
  match size:
    case 0n: {==}
    case 1n+n: {==}

def Laws.bounded_output_empty(budget):
  {==}

def Laws.bounded_output_exact_budget(size):
  match size:
    case 0n: {==}
    case 1n+n:
      %Laws.bounded_output_exact_budget(n) : {1n+M.minimum(n, n) == 1n+_ : Nat}
      {==}

def Laws.stale_schedule_never_recovers(events, enabled, locked):
  match events:
    case Nil{}: {==}
    case Con{M.Pause{}, tail}: Laws.stale_schedule_never_recovers(tail, False{}, locked)
    case Con{M.Resume{}, tail}: Laws.stale_schedule_never_recovers(tail, True{}, locked)
    case Con{M.Edit{}, tail}: Laws.stale_schedule_never_recovers(tail, enabled, locked)
    case Con{M.Tick{}, tail}: Laws.stale_schedule_never_recovers(tail, enabled, locked)

def Laws.paused_schedule_cannot_run(fresh, locked):
  match fresh locked:
    case False{} False{} :
      {==}
    case False{} True{} :
      {==}
    case True{} False{} :
      {==}
    case True{} True{} :
      {==}

def Laws.schedule_requires_lock(enabled, fresh):
  match enabled fresh:
    case False{} False{} :
      {==}
    case False{} True{} :
      {==}
    case True{} False{} :
      {==}
    case True{} True{} :
      {==}

def Laws.handoff_preserves_exclusion(s, e, safe):
  match s e:
    case M.Handoff{False{}, False{}} M.BlockSource{}:
      {==}
    case M.Handoff{False{}, False{}} M.ActivateDestination{}:
      {==}
    case M.Handoff{False{}, False{}} M.BlockDestination{}:
      {==}
    case M.Handoff{False{}, False{}} M.ReturnSource{}:
      {==}
    case M.Handoff{False{}, False{}} M.TransferFailed{}:
      {==}
    case M.Handoff{False{}, True{}} M.BlockSource{}:
      {==}
    case M.Handoff{False{}, True{}} M.ActivateDestination{}:
      {==}
    case M.Handoff{False{}, True{}} M.BlockDestination{}:
      {==}
    case M.Handoff{False{}, True{}} M.ReturnSource{}:
      {==}
    case M.Handoff{False{}, True{}} M.TransferFailed{}:
      {==}
    case M.Handoff{True{}, False{}} M.BlockSource{}:
      {==}
    case M.Handoff{True{}, False{}} M.ActivateDestination{}:
      {==}
    case M.Handoff{True{}, False{}} M.BlockDestination{}:
      {==}
    case M.Handoff{True{}, False{}} M.ReturnSource{}:
      {==}
    case M.Handoff{True{}, False{}} M.TransferFailed{}:
      {==}
    case M.Handoff{True{}, True{}} M.BlockSource{}:
      {==}
    case M.Handoff{True{}, True{}} M.ActivateDestination{}:
      safe
    case M.Handoff{True{}, True{}} M.BlockDestination{}:
      {==}
    case M.Handoff{True{}, True{}} M.ReturnSource{}:
      safe
    case M.Handoff{True{}, True{}} M.TransferFailed{}:
      safe

def Laws.migration_never_runs_both_nodes(events, s, safe):
  match events:
    case Nil{}: safe
    case Con{e, tail}:
      Laws.migration_never_runs_both_nodes(tail, M.handoff_step(s, e), Laws.handoff_preserves_exclusion(s, e, safe))

def Laws.health_stalled_does_not_pass():
  {==}

def Laws.health_uncertain_does_not_pass():
  {==}

def Laws.health_unhealthy_does_not_pass():
  {==}

def Laws.health_progress_can_pass():
  {==}

def Laws.configured_value_wins(value, fallback):
  {==}

def Laws.absent_value_uses_default(fallback):
  {==}

def Laws.flags_override_environment(flag, env, fallback):
  {==}

def Laws.invalid_config_preserves_old(old, new):
  {==}

def Laws.failed_commit_preserves_old(old, staged):
  {==}

def Laws.successful_commit_installs_new(old, staged):
  {==}

def Laws.secrets_are_redacted(secret):
  {==}

def Laws.public_text_is_preserved(value):
  {==}

def Laws.redaction_is_idempotent(text):
  match text:
    case M.Public{value}: {==}
    case M.Secret{value}: {==}
    case M.Redacted{}: {==}

def Laws.unknown_alert_is_rejected(state, severity, bounded):
  {==}

def Laws.events_cannot_fire(severity, bounded):
  match severity:
    case M.Critical{}: {==}
    case M.Warning{}: {==}
    case M.Informational{}: {==}
    case M.InvalidSeverity{}: {==}

def Laws.oversized_alert_is_rejected(kind, state, severity):
  match kind state severity:
    case M.Rule{} M.Firing{} M.Critical{}: {==}
    case M.Rule{} M.Firing{} M.Warning{}: {==}
    case M.Rule{} M.Firing{} M.Informational{}: {==}
    case M.Rule{} M.Firing{} M.InvalidSeverity{}: {==}
    case M.Rule{} M.Recovered{} M.Critical{}: {==}
    case M.Rule{} M.Recovered{} M.Warning{}: {==}
    case M.Rule{} M.Recovered{} M.Informational{}: {==}
    case M.Rule{} M.Recovered{} M.InvalidSeverity{}: {==}
    case M.Rule{} M.Info{} M.Critical{}: {==}
    case M.Rule{} M.Info{} M.Warning{}: {==}
    case M.Rule{} M.Info{} M.Informational{}: {==}
    case M.Rule{} M.Info{} M.InvalidSeverity{}: {==}
    case M.Rule{} M.InvalidState{} M.Critical{}: {==}
    case M.Rule{} M.InvalidState{} M.Warning{}: {==}
    case M.Rule{} M.InvalidState{} M.Informational{}: {==}
    case M.Rule{} M.InvalidState{} M.InvalidSeverity{}: {==}
    case M.Event{} M.Firing{} M.Critical{}: {==}
    case M.Event{} M.Firing{} M.Warning{}: {==}
    case M.Event{} M.Firing{} M.Informational{}: {==}
    case M.Event{} M.Firing{} M.InvalidSeverity{}: {==}
    case M.Event{} M.Recovered{} M.Critical{}: {==}
    case M.Event{} M.Recovered{} M.Warning{}: {==}
    case M.Event{} M.Recovered{} M.Informational{}: {==}
    case M.Event{} M.Recovered{} M.InvalidSeverity{}: {==}
    case M.Event{} M.Info{} M.Critical{}: {==}
    case M.Event{} M.Info{} M.Warning{}: {==}
    case M.Event{} M.Info{} M.Informational{}: {==}
    case M.Event{} M.Info{} M.InvalidSeverity{}: {==}
    case M.Event{} M.InvalidState{} M.Critical{}: {==}
    case M.Event{} M.InvalidState{} M.Warning{}: {==}
    case M.Event{} M.InvalidState{} M.Informational{}: {==}
    case M.Event{} M.InvalidState{} M.InvalidSeverity{}: {==}
    case M.Unrecognized{} M.Firing{} M.Critical{}: {==}
    case M.Unrecognized{} M.Firing{} M.Warning{}: {==}
    case M.Unrecognized{} M.Firing{} M.Informational{}: {==}
    case M.Unrecognized{} M.Firing{} M.InvalidSeverity{}: {==}
    case M.Unrecognized{} M.Recovered{} M.Critical{}: {==}
    case M.Unrecognized{} M.Recovered{} M.Warning{}: {==}
    case M.Unrecognized{} M.Recovered{} M.Informational{}: {==}
    case M.Unrecognized{} M.Recovered{} M.InvalidSeverity{}: {==}
    case M.Unrecognized{} M.Info{} M.Critical{}: {==}
    case M.Unrecognized{} M.Info{} M.Warning{}: {==}
    case M.Unrecognized{} M.Info{} M.Informational{}: {==}
    case M.Unrecognized{} M.Info{} M.InvalidSeverity{}: {==}
    case M.Unrecognized{} M.InvalidState{} M.Critical{}: {==}
    case M.Unrecognized{} M.InvalidState{} M.Warning{}: {==}
    case M.Unrecognized{} M.InvalidState{} M.Informational{}: {==}
    case M.Unrecognized{} M.InvalidState{} M.InvalidSeverity{}: {==}

def Laws.ui_runs_action_once(calls):
  {==}

def Laws.orchestrator_sync_includes_sync_dirs():
  {==}

def Laws.orchestrator_sync_preserves_config():
  {==}

def Laws.orchestrator_sync_preserves_tss():
  {==}

def Laws.orchestrator_sync_preserves_producer():
  {==}

def Laws.orchestrator_sync_preserves_other():
  {==}
