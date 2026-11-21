import Base

# Executable specification. These predicates describe decisions, not the Go
# filesystem, cryptography, RPC transport or operating system themselves.
def change(locked: Bool, validated: Bool, stopped: Bool, recoverable: Bool) -> Bool:
  locked && (validated && (stopped && recoverable))

def publish(complete: Bool, verified: Bool, fresh: Bool, owned: Bool) -> Bool:
  complete && (verified && (fresh && owned))

def authenticate(known: Bool, signed: Bool, fresh: Bool) -> Bool:
  known && (signed && fresh)

def deliver(enabled: Bool, changed: Bool, due: Bool) -> Bool:
  enabled && (changed || due)

def record_delivery(previous: Nat, candidate: Nat, success: Bool) -> Nat:
  match success:
    case False{}: previous
    case True{}: candidate

type Service is Data:
  Active{}
  Inactive{}
  Failed{}
  NotFound{}
  Activating{}
  Deactivating{}
  Reloading{}
  Maintenance{}
  Refreshing{}
  Unknown{}

def stopped(s: Service) -> Bool:
  match s:
    case Inactive{}: True{}
    case Failed{}: True{}
    case NotFound{}: True{}
    case _: False{}

type Path is Data:
  Nom{}
  Network{}
  Consensus{}
  Cache{}
  Log{}
  Wallet{}
  Config{}
  Other{}

def backup_path(p: Path) -> Bool:
  match p:
    case Nom{}: True{}
    case Network{}: True{}
    case Consensus{}: True{}
    case Cache{}: True{}
    case _: False{}

def resync_path(p: Path) -> Bool:
  match p:
    case Nom{}: True{}
    case Network{}: True{}
    case Consensus{}: True{}
    case Log{}: True{}
    case _: False{}

def archive_entry(contained: Bool, safe_type: Bool, allowed: Bool) -> Bool:
  contained && (safe_type && allowed)

# Number of oldest archives pruned after sorting newest first.
def prune_count(count: Nat, keep: Nat) -> Nat:
  match count keep:
    case 0n _: 0n
    case 1n+n 0n: 1n+n
    case 1n+n 1n+k: prune_count(n, k)

def minimum(a: Nat, b: Nat) -> Nat:
  match a b:
    case 0n _: 0n
    case 1n+x 0n: 0n
    case 1n+x 1n+y: 1n+minimum(x, y)

type Schedule is Data:
  Schedule{enabled: Bool, fresh: Bool}
type ScheduleEvent is Data:
  Pause{}
  Resume{}
  Edit{}
  Tick{}

def schedule_step(s: Schedule, e: ScheduleEvent) -> Schedule:
  match s e:
    case Schedule{enabled, fresh} Pause{}: Schedule{False{}, False{}}
    case Schedule{enabled, fresh} Resume{}: Schedule{True{}, False{}}
    case Schedule{enabled, fresh} Edit{}: Schedule{enabled, False{}}
    case _ Tick{}: s

def schedule_run(events: +List<ScheduleEvent>, s: Schedule) -> Schedule:
  match events:
    case Nil{}: s
    case Con{e, tail}: schedule_run(tail, schedule_step(s, e))

def scheduled(s: Schedule, locked: Bool) -> Bool:
  match s:
    case Schedule{enabled, fresh}: fresh && (enabled && locked)

type Handoff is Data:
  Handoff{source: Bool, destination: Bool}
type HandoffEvent is Data:
  BlockSource{}
  ActivateDestination{}
  BlockDestination{}
  ReturnSource{}
  TransferFailed{}

def handoff_step(s: Handoff, e: HandoffEvent) -> Handoff:
  match s e:
    case Handoff{source, destination} BlockSource{}: Handoff{False{}, destination}
    case Handoff{False{}, destination} ActivateDestination{}: Handoff{False{}, True{}}
    case Handoff{True{}, destination} ActivateDestination{}: Handoff{True{}, destination}
    case Handoff{source, destination} BlockDestination{}: Handoff{source, False{}}
    case Handoff{source, False{}} ReturnSource{}: Handoff{True{}, False{}}
    case Handoff{source, True{}} ReturnSource{}: Handoff{source, True{}}
    case _ TransferFailed{}: s

def handoff_run(events: +List<HandoffEvent>, s: Handoff) -> Handoff:
  match events:
    case Nil{}: s
    case Con{e, tail}: handoff_run(tail, handoff_step(s, e))

def dual_active(s: Handoff) -> Bool:
  match s:
    case Handoff{source, destination}: source && destination

type Outcome is Data:
  Healthy{}
  Syncing{}
  Stalled{}
  Uncertain{}
  Unhealthy{}

def passed(o: Outcome) -> Bool:
  match o:
    case Healthy{}: True{}
    case Syncing{}: True{}
    case _: False{}

def progress(stable: Bool, reachable: Bool, identified: Bool, increasing: Bool) -> Bool:
  stable && (reachable && (identified && increasing))

def effective(configured: Maybe<&2, Nat>, fallback: Nat) -> Nat:
  match configured:
    case None{}: fallback
    case Some{x}: x

def precedence(flag: Maybe<&2, Nat>, env: Maybe<&2, Nat>, fallback: Nat) -> Nat:
  effective(flag, effective(env, fallback))

def save(old: Nat, new: Nat, valid: Bool) -> Nat:
  match valid:
    case False{}: old
    case True{}: new

type Text is Data:
  Public{value: Nat}
  Secret{value: Nat}
  Redacted{}

def redact(t: Text) -> Text:
  match t:
    case Secret{value}: Redacted{}
    case _: t

def expose(t: Text) -> Bool:
  match t:
    case Secret{value}: True{}
    case _: False{}

def ready(history: Bool, current: Bool, continuous: Bool, complete: Bool) -> Bool:
  history && (current && (continuous && complete))

type AlertKind is Data:
  Rule{}
  Event{}
  Unrecognized{}
type AlertState is Data:
  Firing{}
  Recovered{}
  Info{}
  InvalidState{}
type Severity is Data:
  Critical{}
  Warning{}
  Informational{}
  InvalidSeverity{}

def valid_alert(kind: AlertKind, state: AlertState, severity: Severity, bounded: Bool) -> Bool:
  match kind state severity:
    case Unrecognized{} _ _: False{}
    case Event{} Info{} Informational{}: bounded
    case Event{} _ _: False{}
    case Rule{} InvalidState{} _: False{}
    case Rule{} _ InvalidSeverity{}: False{}
    case Rule{} _ _: bounded

def step_count(calls: Nat) -> Nat:
  1n+calls

# Reversible writes retain the previous value on an unsuccessful commit.
def commit(old: Nat, staged: Nat, success: Bool) -> Nat:
  match success:
    case False{}: old
    case True{}: staged
