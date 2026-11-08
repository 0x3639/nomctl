# Menu and source upgrade implementation

1. Extract build-only source preparation and share the existing safe upgrade
   transaction with source builds; default to source, opt into releases with
   `--signed`, and test mode-specific flags.
2. Define grouped menus, direct action dispatch and persistent submenu navigation;
   separate backup scheduling, wire nomctl self-upgrade, and share source pickers.
3. Update the guides, command reference, README and update-available hints.
4. Verify navigation and source failure paths, then run tests, lint, docs build
   inside nodebox, and Linux cross-builds.
