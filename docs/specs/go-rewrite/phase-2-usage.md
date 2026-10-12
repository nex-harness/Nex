# Phase 2 usage: running the Go `nex-server`

Step 2.11 of [the Phase 2 plan](phase-2-go-server.md). This file is the operational documentation for
`go/cmd/nex-server`: how to build it, what environment it reads, where it gets its Node upstream,
and what to check when it misbehaves.

It is written for someone standing up a deployment or debugging one. The design rationale lives in
the Go source and in the phase plan; this file is the part you need without reading either.

## What this binary is

`nex-server` is the Go front of the strangler rewrite. It is the **only** process the browser or
the Desktop app talks to:

| Concern                                             | Owner in Phase 2                     |
| --------------------------------------------------- | ------------------------------------ |
| Static Web UI, SPA fallback                         | Go (`internal/server/static.go`)     |
| `GET /api/server-info`                              | Go (`internal/server/serverinfo.go`) |
| Web authentication (`/api/auth/*`)                  | Go (`internal/server/webauth`)       |
| `/ws`, `/ws/host`, host capability                  | Go (`internal/server/ws.go`)         |
| Routing table, upstream forwarding, proxying        | Go (`internal/gateway`)              |
| **Every service channel**                           | **Node `@nex/server` child process** |
| `/api/pets/*`, `/api/bots/*`, `/api/connect-remote` | Node, reached through the Go proxy   |

So the deployment is two processes: the Go binary, and a Node `@nex/server` it supervises. The Go
binary starts the Node one itself; you do not start it separately.

## Build

```bash
scripts/go-rewrite/build-server.sh              # all five targets into go/dist/
scripts/go-rewrite/build-server.sh linux-amd64   # one target
DIST_DIR=/tmp/out scripts/go-rewrite/build-server.sh
```

Output layout is `go/dist/<os>-<arch>/nex-server[.exe]`:

```
linux-amd64    linux-arm64    darwin-arm64    darwin-amd64    windows-amd64
```

The build is reproducible: `-trimpath`, `-buildvcs=false`, a `SOURCE_DATE_EPOCH` pinned to the
latest commit's committer time, and `-ldflags "-s -w"`. Two runs on the same commit produce
byte-identical binaries, so a published sha256 is checkable. `CGO_ENABLED=0` on every target, which
is what makes the Linux binary run on `debian:bookworm-slim` with no libc dependency
(`ldd go/dist/linux-amd64/nex-server` prints `not a dynamic executable`).

The version is injected, not hardcoded: `-X …/internal/server.Version=<root package.json version>`.
`nex-server --version` prints the same value the Node bundle reports through `__NEX_VERSION__`.

### Release layout the binary expects

The Go binary looks for its Node upstream in this order, first match wins:

| Order | Directory                               | Layout it assumes            |
| ----- | --------------------------------------- | ---------------------------- |
| 1     | `$NEX_SERVER_ROOT/runtime/`             | a `nex-server-cli` release   |
| 2     | `<dir of the binary>/server/`           | the distribution tarball     |
| 3     | `<dir of the binary>/` and `../server/` | a bare binary next to `web/` |
| 4     | `<repo>/packages/server/dist/`          | a developer checkout         |

Within a directory it accepts `nex-server-http.cjs`, `server-core.js`, `entry-http.js`, and a SEA
binary named `nex-server`/`nex-server.exe`. A `.js` entry is run as `node <entry>` (resolved from
`PATH`); a SEA binary is executed directly, because it is its own interpreter.

If none of them exists the server **refuses to start** and names every directory it searched. That
is deliberate: the gateway forwards by default, so a server without an upstream could only serve
routes Go does not own yet, which is none of them.

## Environment

Every variable below already exists in the TypeScript server. **No new environment variable is
introduced** (decision D7) — the gateway's routing configuration is a file, not an env var.

| Variable                | Read by                        | Meaning                                                                                                   |
| ----------------------- | ------------------------------ | --------------------------------------------------------------------------------------------------------- |
| `PORT`                  | Go, and the Node upstream      | Listen port. Non-numeric or `0` falls back to `3030` (`Number(PORT) \|\| 3030`).                          |
| `NEX_SERVER_HOST`       | Go, and the Node upstream      | Listen host. Falls back to `HOST`, then to all interfaces.                                                |
| `NEX_WEB_STATIC_ROOT`   | Go, and the Node upstream      | Directory of the built web client. **Unset means no static serving at all** — there is no default `web/`. |
| `NEX_SERVER_AUTH_TOKEN` | Go                             | Web login token. Unset means authentication is disabled. Whitespace-only counts as unset.                 |
| `NEX_DATA_BASE_DIR`     | Go, and the Node upstream      | Data base. Defaults to `$HOME`. `web-sessions.json` lives at `<base>/.nex/v2/`.                           |
| `NEX_SERVER_WORKSPACE`  | Go, and the Node upstream      | Workspace advertised by `GET /api/server-info`. Unset advertises none.                                    |
| `NEX_SERVER_ID`         | Go, and the Node upstream      | Overrides the derived server id (this value, then hostname, then `nex-server`).                           |
| `NEX_SERVER_NAME`       | Go, and the Node upstream      | Display name. Omitted from server-info when empty.                                                        |
| `NEX_SERVER_ROOT`       | Node upstream resolution       | Release root; the upstream is looked up under `runtime/`.                                                 |
| `GLM_BINARY_PATH`       | The Node upstream's agent code | See below.                                                                                                |

### Static web root

`NEX_WEB_STATIC_ROOT` is the only thing that turns on static serving, and it is the same rule the
TypeScript server uses: `options.staticRoot?.trim()`, so whitespace-only means **off**. The Go front
owns static requests outright — the gateway does not proxy them — so the web UI is always served
from the Go process at `/`, with the SPA fallback for unknown paths.

A development build from a checkout:

```bash
pnpm --filter @nex/web build
go/dist/linux-amd64/nex-server   # with NEX_WEB_STATIC_ROOT=$PWD/packages/web/dist
```

### `GLM_BINARY_PATH`

`GLM_BINARY_PATH` is **not read by the Go front at all.** It is read by the Node upstream, inside
the code that resolves which `nex-agent` binary to spawn, and it is honoured there exactly as it is
today:

- `packages/shared/src/nex-agent-runtime.ts` declares it as `NEX_AGENT_RUNTIME.binaryEnvVar`;
- `providerRuntimeResolver.ts` consults it after `resolveDeployedNexAgentBinaryCommand` and before
  `findNexAgentRuntimeBinary`'s bundled-resource lookup.

Two consequences that matter in practice:

1. **It works through the Go front unchanged.** The Go server inherits its environment into the
   Node upstream and does not override `GLM_BINARY_PATH`, so pointing it at the Go agent makes the
   upstream spawn the Go agent — the same opt-in Phase 1 uses.
2. **The working directory matters.** The resolution walks upward looking for a bundled agent
   first, so running from inside this monorepo means the bundled bundle is found and
   `GLM_BINARY_PATH` is never consulted. `scripts/go-rewrite/e2e-agent-smoke.mjs` deliberately runs
   from outside the repository for exactly this reason. Set the variable _and_ run from outside the
   repo, or the variable will appear to be ignored.

## Running it

```bash
NEX_WEB_STATIC_ROOT=/path/to/web/dist \
NEX_SERVER_AUTH_TOKEN=$(openssl rand -base64 32) \
NEX_DATA_BASE_DIR=/var/lib/nex \
NEX_SERVER_WORKSPACE=/workspace \
NEX_SERVER_ROOT=/opt/nex/current \
  /usr/local/bin/nex-server
```

Startup logs one `info` line describing the deployment — address, version, whether auth is on,
static root, workspace, data base, routing-table source and upstream address. That line is the first
thing to read when something is wrong.

`nex-server --version` prints the injected version. `nex-server --help` lists the environment.

### Shutdown

`SIGTERM` and `SIGINT` both stop it in this order: the listener is closed and in-flight requests are
drained (5 s), then the Node process tree is sent `SIGTERM`, escalating to `SIGKILL` after the grace
period, and the process waits for an observed terminal state before exiting. `SIGKILL` on the Go
process alone will orphan the Node child — the tree kill is what prevents that.

## State on disk

| Path                                      | Owner             | Contents                                                             |
| ----------------------------------------- | ----------------- | -------------------------------------------------------------------- |
| `<data base>/.nex/v2/web-sessions.json`   | **Go front**      | Web login sessions, hashed, mode `0600`.                             |
| `<data base>/.nex/v2/gateway-routes.json` | Go front          | Optional routing table. Missing = everything goes to Node.           |
| `<data base>/.nex/v2/node-upstream/`      | **Node upstream** | Everything the upstream owns: provider config, its own session file. |

The two processes deliberately do **not** share a data base. `entry-http.ts` always writes
`web-sessions.json` under `<data base>/.nex/v2`, so an upstream sharing the directory would open the
same file under a _different_ token (the supervisor generates one per start), and
`webAuth`'s store discards sessions whose fingerprint does not match its own — correct for a token
rotation, and here it would log every user out at random. The upstream is loopback-only and the Go
front authenticates every client itself, so it never needs a session of its own.

## Routing table (optional)

`<data base>/.nex/v2/gateway-routes.json` is the only configuration the strangler needs. A missing
file is not an error — every channel goes to Node, which is the correct state until a service is
ported:

```json
{ "version": 1, "default": "node", "channels": { "file": "go", "git": "node" } }
```

`default` (absent = `node`) is the target for any channel the table does not name; `http` maps route
prefixes the same way. The table is read once at startup, so an edit does not half-apply to live
connections — restart to apply it.

Unknown version, unknown target, duplicate key, misspelled key, or trailing content after the JSON
object is a **startup error**, not a warning: continuing with a table we ignored would move channels
between processes without anyone deciding to. A channel the table names that is not a real service
channel is also rejected, because a typo that silently kept a channel on Node forever is worse than
a refusal to start.

Channels Go handles itself always win over the table: the table can move a channel ONTO Go, never
OFF it, because a ported handler that exists but is unrouted would otherwise be served from Node.

## Verifying a deployment

```bash
bash scripts/go-rewrite/docker-accept.sh
```

Runs the acceptance list from [docker-single-image.md](../docker-single-image.md) against the Go
front in a throwaway directory: the exact `/api/auth/session` body, `/` serving the page,
unauthenticated `/api/*` returning 401, login, WebSocket upgrade, session survival across a restart,
and a static-link check. `--no-repo-checks` skips the slower repository checks; `--front node` runs
the same list against the TypeScript server for comparison.

It does **not** build or publish a Docker image — Phase 2 leaves the published image unchanged.
