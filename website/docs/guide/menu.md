---
title: The interactive menu
---

```bash
sudo nomctl
```

With no arguments nomctl opens a menu covering every action:

| Entry | Runs |
|---|---|
| deploy | [Deploy](/guide/deploy), with repository and branch pickers |
| restart, stop, start | [Service control](/guide/service) |
| monitor | `logs -f` |
| status | [`top`](/troubleshooting/first-look), the live dashboard |
| alerts | [Alerts](/alerts/overview) setup, or status when already paired |
| support | [Support bundle](/troubleshooting/support-bundle) |
| resync | Resync, after a confirmation |
| backup | Backup, then an offer to schedule |
| restore | Restore, with an archive picker |
| analytics | [Analytics stack](/guide/analytics) |

After each action the menu asks whether to return. Esc or Ctrl+C leaves.

The menu needs a terminal. Over a plain pipe nomctl refuses and points at the subcommands, which behave identically and are what scripts should use.
