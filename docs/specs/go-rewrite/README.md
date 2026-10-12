# Go rewrite of nex-server and the agent — master plan

Status: proposed. Nothing in this directory is implemented yet.
Scope: everything that is not Web UI or Desktop UI.

| In scope (rewritten in Go)                                                                                                                                    | Out of scope (stays TypeScript)                              |
| ------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------ |
| `packages/server`, `packages/services`, `packages/rpc`, `packages/nex-server-cli`, `packages/provider`, `packages/provider-node`, `packages/model-option-map` | `packages/ui`, `packages/web`, `packages/desktop` (UI parts) |
| `apps/nex-cli/packages/{core,adapters,bootstrap,contracts,i18n}`                                                                                              | `packages/formal-proof`                                      |
| The Go side of `packages/shared` (protocol types) and `packages/client` (test client only)                                                                    | The UI-facing TypeScript types in `packages/shared`          |

`apps/nex-cli/packages/{tui,cli}` (terminal front-ends) are **undecided** — see decision D6.

## How to read these docs

Each phase has one document: `phase-N-*.md`. A phase document contains:

1. **Goal / non-goals** — what is true when the phase is finished.
2. **Entry criteria** — what must already be true. A phase must not start before its entry gate passes.
3. **Steps** — numbered `N.k`. Every step lists `Source` (TypeScript to port or read), `Target`
   (Go path), `Tests` (what proves it), and `Done when` (an observable, checkable condition).
4. **Exit gate** — a list of numbered checks `GN.m`, each with an exact command and pass condition.
   The gate is executed by one script: `scripts/go-rewrite/gate.sh <N>` (created in step 0.12).
   It prints `PASS GN.m` / `FAIL GN.m` per check and exits non-zero if any check fails.
5. **Rollback** — how to return to the previous state without data loss.
6. **Verify-first list** — facts the plan assumes but that were **not** confirmed while writing it.
   Do these before depending on them.

A phase is finished only when `scripts/go-rewrite/gate.sh N` exits 0 on a clean checkout, in CI, on
Linux, macOS and Windows runners (checks marked `[linux]` are exempt from the matrix).

## Phase map

| Phase | Document                                             | Outcome                                                                                                                   | Depends on                            | Rough size\* |
| ----- | ---------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------- | ------------------------------------- | ------------ |
| 0     | [phase-0-contracts.md](phase-0-contracts.md)         | Frozen contracts, golden traces, fixtures, replay harness, gate runner. No product code.                                  | —                                     | 3–5 weeks    |
| 1     | [phase-1-go-agent.md](phase-1-go-agent.md)           | A Go `nex-agent` that the existing host can spawn instead of the Node agent, covering the core chat/edit/permission loop. | 0                                     | 3–5 months   |
| 2     | [phase-2-go-server.md](phase-2-go-server.md)         | A Go server that terminates `/ws/host` + HTTP and forwards un-ported channels to the Node server.                         | 0 (1 for the agent-service port in 3) | 4–6 weeks    |
| 3     | [phase-3-services.md](phase-3-services.md)           | Services ported channel by channel behind per-channel switches; Go server runs the Go agent itself.                       | 1, 2                                  | 3–5 months   |
| 4     | [phase-4-agent-breadth.md](phase-4-agent-breadth.md) | Remaining agent features: subagents, hooks, memory, MCP, plugins, web tools, background work, workflow sidecar.           | 1 (parallel with 3)                   | 3–5 months   |
| 5     | [phase-5-cutover.md](phase-5-cutover.md)             | Go is the default; packaging, install, remote deploy, Docker updated; Node kept as fallback for one release.              | 3, 4                                  | 6–8 weeks    |

\* Rough sizes assume a small dedicated team and are estimates, not commitments. The backend today
is roughly 300k lines of non-test TypeScript. Phases 2 and 4 can run in parallel with 3.

```
Phase 0 ──► Phase 1 ──────────────┐
   │                              ├──► Phase 3 ──┐
   └──────► Phase 2 ──────────────┘              ├──► Phase 5
              Phase 1 ──► Phase 4 ───────────────┘
```

## Architecture being replaced (reference)

```
UI (packages/ui, web, desktop)                      stays TypeScript
   │  WebSocket /ws/host — binary channel RPC (packages/rpc)  + HTTP /api/*
Server + services (packages/server, services, nex-server-cli, provider*, shared)
   │  stdio, one JSON message per LF-terminated line — "Nex Protocol" (legacy methods + v4 topics)
Agent (apps/nex-cli: core, adapters, bootstrap, contracts, …)
   │  model HTTP APIs, shell, files, MCP, plugins, browser
```

Two seams make an incremental rewrite possible, and the plan is built around them:

- **Agent seam.** The host spawns the agent with `app-server --stdio`
  (`NEX_AGENT_RUNTIME.spawnArgs` in `packages/shared/src/nex-agent-runtime.ts`). The binary path comes from
  `NexAgentCommandResolver` (`packages/services/src/nex-agent/nexAgentProcessManager.ts`); the
  `GLM_BINARY_PATH` variable overrides the bundled binary
  (`packages/services/src/runtime-tools/providerRuntimeResolver.ts`). A Go binary that speaks the same
  stdio protocol replaces the Node agent without touching the UI or the TypeScript host.
- **RPC seam.** The UI talks to the server through named channels over a transport-agnostic RPC
  (`ChannelServer`). A Go front server can serve some channels itself and forward the rest to the
  Node server (strangler pattern), one channel at a time.

## Decisions (resolve in Phase 0 step 0.2; defaults shown)

| ID  | Decision             | Default                                                                                                                                                                                                                                  | Why / consequence                                                                                                                                                                                                     |
| --- | -------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| D1  | Go source location   | `go/` at the repository root, single module                                                                                                                                                                                              | `pnpm-workspace.yaml` only globs `packages/*` and `apps/nex-cli*`, so `go/` is invisible to pnpm. Module path is a placeholder (`github.com/tylinux/nex/go`) — confirm the real path before the first commit.         |
| D2  | Go toolchain         | `go 1.24` in `go.mod` (1.24.4 is installed on the dev host)                                                                                                                                                                              | Pin in `mise.toml` alongside node/pnpm in step 0.3.                                                                                                                                                                   |
| D3  | SQLite driver        | `modernc.org/sqlite` (pure Go, no cgo)                                                                                                                                                                                                   | Keeps cross-compilation trivial (Windows/macOS/Linux, x64/arm64). Performance must be benchmarked in Phase 1 (step 1.6). The Node side uses `node:sqlite`.                                                            |
| D4  | JS-bound features    | Keep as Node **sidecar** processes: `dynamic-workflow`, `codemode`/`repl`, `node-repl-host`, Browser Use, CUA                                                                                                                            | They depend on the `typescript` compiler, QuickJS WASM, Playwright. The Go distribution then still bundles Node for them. Alternative: drop them. Must be decided before Phase 4.                                     |
| D5  | Parity oracle        | Recorded TypeScript behavior (golden traces), not the existing unit tests                                                                                                                                                                | The repo has few tests relative to its size (counted: 14 files in `packages/services/test`, 12 under `apps/nex-cli/packages`, 1 in `packages/server/test`).                                                           |
| D6  | TUI and `nex` CLI    | Not ported in this plan. They talk to `@nex/bootstrap`; keep a thin Node TUI that spawns the Go agent over the same protocol, or defer                                                                                                   | Needs a product decision. If "no Node at all" is a requirement, add a Phase 6.                                                                                                                                        |
| D7  | Cutover switch       | No new environment variables. Use the existing `GLM_BINARY_PATH` for the agent and a config-file routing table for server channels                                                                                                       | `apps/nex-cli/AGENTS.md` forbids new env vars without a spec.                                                                                                                                                         |
| D8  | Shared on-disk state | Node and Go read/write the **same** data directory during the transition; migrations are append-only and must stay readable by both                                                                                                      | Schema changes need maintainer sign-off (`apps/nex-cli/AGENTS.md`).                                                                                                                                                   |
| D10 | Desktop local host   | **A:** Desktop keeps its Node utility-process host (it calls `createLocalServices` in-process, `packages/desktop/src/host/index.ts`) and only switches to the Go agent. **B:** a Go local host replaces it (months, separate `phase-6`). | Found while planning Phase 5: Desktop does not use `nex-server`, so Phases 2–3 benefit Web/server/Docker directly but Desktop only via the Go agent unless B is chosen. Resolve in step 5.2 (earlier if B is likely). |
| D9  | Wire compatibility   | Go must be byte-compatible where the UI/host parses bytes (RPC serialization, v4 wire), and semantically compatible elsewhere                                                                                                            | Enforced by differential tests in Phases 2 and 3.                                                                                                                                                                     |

## Conventions for all Go code

Mirror the repository rules (`AGENTS.md`, `apps/nex-cli/AGENTS.md`) so the Go code is reviewable by the same people:

- Spec before behavior change; keep these docs current when a step's scope changes.
- Files under 400 lines; split by cohesion. `go/` is exempt from the TypeScript architecture checker
  but gets the same limit via a lint rule (step 0.3).
- External I/O only in adapter packages (`internal/adapters/...`): network, files, subprocesses, env.
  Business packages depend on interfaces. A `depguard`-style lint rule enforces it.
- Every task carries a `traceId`; `sessionId`, `turnId`, `messageId`, `toolCallId`, `spanId`,
  `parentSpanId` hang under it. Pass it through `context.Context` (`internal/trace`).
- Workspace identity key is `workspaceIdentity` trimmed, falling back to `workspacePath`.
  Never key by path alone for remote workspaces.
- Logging: structured, to stderr (the agent's stdout is reserved for protocol frames). Levels follow
  `AGENTS.md`: debug for chunks/raw protocol, info for lifecycle, warn for recoverable, error for fatal.
- No credentials, real user data or internal URLs in fixtures. Fixtures are produced with throwaway
  `HOME` directories and fake keys, and a scrubber check (step 0.9) fails the build on secret-shaped strings.
- Windows, macOS, Linux all supported from the first commit: no hard-coded separators, no shell strings,
  argument-array process spawning, explicit handling of `.exe`/`.cmd`.
- Any behavior change in the TypeScript stack must keep `pnpm typecheck`, `pnpm lint` and
  `pnpm architecture:check --changed` passing. These are part of every gate (check `GN.0`).

## Baseline environment (observed while writing this plan)

- `go1.24.4 linux/amd64` and `node v24.20.0` are installed. `mise.toml` pins Node 24.14.0 and pnpm 10.33.2.
- `pnpm` is **not** installed and `node_modules` is absent, so no TypeScript command was run while
  writing these docs. Step 0.1 installs the toolchain and records the real baseline.
- `sqlite3` CLI is not installed; use Go or `node:sqlite` for database inspection.
- The checkout is not a git repository from this shell (git reports "dubious ownership" for
  `/mnt/MX500/Developer/Nex`); fix with `git config --global --add safe.directory` before using git-based steps.

## Common gate check `GN.0` (every phase)

```bash
node scripts/check-workspace-freshness.mjs
pnpm typecheck
pnpm lint
pnpm architecture:check --changed
(cd go && gofmt -l . | tee /dev/stderr | wc -l | grep -qx 0)   # no unformatted files
(cd go && go vet ./... && go test -race -count=1 ./...)
(cd go && GOOS=windows GOARCH=amd64 go build ./... && GOOS=darwin GOARCH=arm64 go build ./...)
```

Pass condition: every command exits 0. Report real output; an already-failing baseline check
(recorded in step 0.1) is listed as a known failure, never reported as passing.
