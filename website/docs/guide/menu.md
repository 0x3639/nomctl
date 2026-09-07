---
title: The interactive menu
description: "The interactive menu that wraps every nomctl command."
---

```bash
sudo nomctl
```

Opening the menu does not run hardware, NTP or Internet pre-flight checks. Those checks run when you choose node deployment, Pillar deployment or analytics setup. Service controls, configuration, diagnostics and recovery remain accessible during an outage. `--skip-preflight` also skips the setup checks.

With no arguments nomctl opens a menu covering every action:

| Entry | Runs |
|---|---|
| pillar-deploy | [Deploy a Pillar](/guide/pillar): build the official go-zenon master, start the node, create the producer key |
| deploy | [Deploy](/guide/deploy) a plain node, with repository and branch pickers |
| restart, stop, start | [Service control](/guide/service) |
| monitor | `logs -f` |
| status | [`top`](/troubleshooting/first-look), the live dashboard |
| alerts | [Alerts](/alerts/overview) setup, or status when already paired |
| support | [Support bundle](/troubleshooting/support-bundle) |
| resync | Resync, after a confirmation |
| backup | Backup, then an offer to schedule |
| restore | Restore, with an archive picker |
| bootstrap | [Bootstrap](/guide/bootstrap), with URL prompt, keep/discard question and confirmation |
| analytics | [Analytics stack](/guide/analytics) |
| pillar | [Pillar](/guide/pillar) submenu: deploy a Pillar, set up the producer key, show it |
| config | [config.json editor](/guide/config) submenu: show, set one setting, edit in `$EDITOR`; offers a restart |
| orchestrator | [Orchestrator](/guide/orchestrator) submenu: hard reset, status, logs |

After each action the menu asks whether to return. Esc or Ctrl+C leaves.

The menu needs a terminal. Over a plain pipe nomctl refuses and points at the subcommands, which behave identically and are what scripts should use.
