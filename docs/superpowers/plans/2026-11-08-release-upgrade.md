# Signed node release upgrade implementation

1. Add authenticated release discovery/download and pinned GPG/checksum
   verification in `internal/nodeupgrade`.
2. Extract reusable backup preparation, capture and completion phases, keeping
   ordinary backup behaviour and using unique retained staging directories.
3. Implement the locked upgrade transaction with atomic install, service
   checks, progress messages and recoverable failure paths.
4. Add the CLI command, release picker, token configuration and documentation.
5. Test verification, operation ordering and failure paths; run formatting,
   Go tests/vet, documentation validation in nodebox, and Linux cross-builds.
