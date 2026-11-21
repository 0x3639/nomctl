# Build and dependency policy

Node.js dependency installation and any execution of third-party package code
must happen inside a container, never directly on the host.

A host-installed helper, `nodebox`, wraps this. It mounts only the current
project directory, runs as a non-root user, drops all capabilities, and exposes
no host credentials or docker socket.

| Instead of | Run |
|---|---|
| `npm install` | `nodebox npm install` |
| `npm ci` | `nodebox npm ci` |
| `npm run <script>` | `nodebox npm run <script>` |
| `npx <tool>` | `nodebox npx <tool>` |
| `node <file>` | `nodebox node <file>` |

To run untrusted code with networking disabled: `nodebox --offline node <file>`.

- Never run `npm install`, `npm ci`, `npx`, or a `node` process that loads
  `node_modules` directly on the host.
- If `nodebox` is unavailable, say so and stop; do not fall back to the host.
- Reading and editing source files on the host is fine. The restriction is on
  executing dependency code.
- Long-running services should use `docker compose` rather than a host process.
- Ports must be published explicitly (`-p 3000:3000`) to be reachable from macOS.
- Only paths under `/Users/user` can be mounted into local containers; Colima
  shares the home directory into its VM and nothing outside it.
- For dev servers, use `nodebox -p 3000 npm run dev`. Bind the server to
  `0.0.0.0`; `HOST=0.0.0.0` is set automatically, but Vite needs `-- --host`.
  Host ports bind to `127.0.0.1` only.

# Bend

When using Bend:
- run `bend guide` to learn it
- use `LAWS.bend` to keep important rules
- run `bend PROOF.bend` before committing
- parallelize the code whenever possible

Use `make bend-image` and `export PATH="$PWD/scripts:$PATH"` to run the pinned
containerized Bend command. Run `make laws` before committing: it checks the
proofs, coverage inventory, model comparisons, and implementation tests.
See `docs/BEND.md` for the contract map and verification boundary.

Every maintained source/configuration area must have a contract entry in
`test/bend/coverage.json`, with named laws and executable evidence. New files
outside the inventory must fail the coverage check. Update laws, models and
implementation checks together; never weaken a law just to make a check pass.
Keep proofs free of unsafe definitions, foreign code and unfinished claims.
Parallelize independent checks and pure work; preserve ordering around locks,
service shutdown, recovery publication and migration handoff.
