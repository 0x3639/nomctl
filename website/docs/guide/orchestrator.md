---
title: Orchestrator
description: "Operate the orchestrator service that runs next to a pillar: hard reset its queues and events, check status, follow its logs."
---

The orchestrator is a separate service, usually installed on a pillar's node. nomctl groups its functions under **Orchestrator** in the menu and `nomctl orchestrator` on the command line. Every function first checks that the orchestrator unit exists and refuses on a node without it.

```bash
sudo nomctl orchestrator status        # active / inactive / not found
sudo nomctl orchestrator logs -f       # follow the journal, Ctrl+C to stop
sudo nomctl orchestrator hard-reset    # see below
```

## Hard reset

Ports the orchestrator hard-reset script. When the orchestrator is stuck, its queued work and event log are discarded and rebuilt from the chain:

1. Stop the orchestrator unit.
2. Wait ten seconds for it to settle.
3. Delete `queues/` and `events/` under the orchestrator directory. Everything else there, including its configuration, stays.
4. Start the unit again.

The menu confirms before starting. The command line does not, like `resync`. A directory that does not exist is skipped, and if a delete fails the orchestrator is still started again and the error reported.

## Settings

| Variable | Default | |
|---|---|---|
| `NOMCTL_ORCHESTRATOR_SERVICE` | `orchestrator` | systemd unit, without `.service` |
| `NOMCTL_ORCHESTRATOR_DIR` | `/root/.orchestrator` | state directory the hard reset cleans |
