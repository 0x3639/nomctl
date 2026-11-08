# Grouped menus and source upgrades

Replace the flat menu with Manage node, Backups & recovery, Install & upgrade,
Pillar & orchestrator, Alerts & tools, and Exit. Submenus display readable labels,
a breadcrumb, explanations for the highlighted action, and Back. Esc returns one
level. Completed/cancelled operations return to the same submenu. Existing CLI
commands stay available. Wallet actions move into Backups & recovery; Pillar and
orchestrator operations are direct entries in their shared group. Configuration
keeps its existing specialized picker.

Use the requested labels verbatim: Backup chain data locally; Restore local
chain data; Restore from "trusted" bootstrap snapshot; Deploy a node from source;
Upgrade a node from source; Upgrade a node from signed release. Bootstrap trust
is an operator decision: the existing hash verifies integrity, not publisher
identity. No new bootstrap trust claim or verification change is introduced.

`nomctl upgrade-node [--repo URL] [--branch NAME] [--yes]` upgrades from source
by default. `nomctl upgrade-node --signed [--version TAG] [--yes]` selects signed
releases explicitly. `--version` requires `--signed`; repository/branch flags
cannot be combined with `--signed`. There is no `--source` flag or release-only
command alias. Source upgrades require an installed binary and service.
Reuse dependency/toolchain setup and a shared build-only helper from deploy.
Clone into a unique temporary checkout, record its commit, and finish building
before downtime. A failed clone/build/cancellation never stops the service.
Then reuse the signed updater's atomic install and pre-upgrade backup transaction:
stop, copy chain data, preserve old binary, install, start, finish archive. Preserve
the existing service unit. Source builds do not claim release-signature validation.

Source selection in the menu reuses deployment's repository/branch pickers and
adds confirmation before acquiring the operation lock. Automated source upgrades
use the configured repository/branch unless flags override them. The lock spans
build and installation. Tests stub git/Go/systemctl to prove build-before-stop and
no downtime on failures. Update all menu navigation docs, CLI references, source
upgrade guidance and update-available suggestions.
