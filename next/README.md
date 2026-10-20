# `next/` MVP — Go server + remote pi runners

The next-generation topology for ZCode: one Go server on a Linux box, the
existing web client (`packages/web` + `packages/ui`, unchanged), and any number
of Node 24 agent runners that dial in and serve pi-backed sessions.

Spec of record: [`docs/next/README.md`](../docs/next/README.md) (channels
research: [`docs/next/channels-research.md`](../docs/next/channels-research.md)).

MVP acceptance: log in → pick a workspace on a connected runner → new chat →
send a prompt → see streamed assistant text and tool cards → the session shows
in the sidebar → reload and reopen it.

## Layout

| Path                          | What it is                                                                                                     |
| ----------------------------- | -------------------------------------------------------------------------------------------------------------- |
| `next/server`                 | Go server: auth, runner registry, workspace routing, UI settings, `/ws` + `/runner` links.                     |
| `packages/next-runner`        | Node 24 runner: pi sessions, v4 conversation projection, sessions index, model catalog.                        |
| `packages/web`, `packages/ui` | Existing web client and components, unchanged; Vite supports `NEX_WEB_DEV_PORT` / `NEX_WEB_DEV_SERVER_ORIGIN`. |
| `next/e2e/acceptance.mjs`     | Playwright acceptance E2E against a running dev stack.                                                         |

Read [`next/server/README.md`](server/README.md) for the server HTTP surface and
routing table, and [`packages/next-runner/README.md`](../packages/next-runner/README.md)
for the runner link protocol, channels and env vars.

## Quick start (local dev)

Requirements: Go (1.27+), Node 24 (see `mise.toml`), pnpm 10, Chrome or
Chromium for the E2E.

```sh
next/dev.sh          # builds the server, starts server + runner + Vite
next/dev.sh --e2e    # same, waits until healthy, then runs next/e2e/acceptance.mjs
```

`dev.sh` creates a demo workspace (`$TMPDIR/nex-next-demo` with `notes.txt`
containing `hello from demo`) unless `NEX_DEMO_WORKSPACE` is set, builds the Go
server into `next/server/bin/` (gitignored), and starts:

- the server on `NEX_NEXT_PORT` (default `3031`), data in
  `$NEX_NEXT_DATA/server` (default `~/.nex-next/server`);
- the runner via `pnpm --filter @nex/next-runner dev`, data in
  `$NEX_NEXT_DATA/runner` (`runner-e2e` for `--e2e`, reset per run);
- Vite on `NEX_NEXT_WEB_PORT` (default `5174`), proxying `/api` and `/ws` to the
  server through `NEX_WEB_DEV_SERVER_ORIGIN`.

Tokens come from `NEX_NEXT_BROWSER_TOKEN` / `NEX_NEXT_RUNNER_TOKEN` (or the
server-side `NEX_SERVER_AUTH_TOKEN` / `NEX_RUNNER_TOKEN`); otherwise random ones
are generated and the browser token is printed once to the terminal. Tokens are
never written to a tracked file. Ctrl-C stops all children.

Open <http://localhost:5174>, sign in with the printed browser token.

## Running a runner on another machine

The server only needs to reach the runner over WebSocket (the runner dials out,
so the runner can be behind NAT).

```sh
cd packages/next-runner
NEX_SERVER_URL=ws://<server-host>:3031/runner \
NEX_RUNNER_TOKEN=<runner token from the server> \
NEX_RUNNER_WORKSPACES=/abs/path/one,/abs/path/two \
NEX_RUNNER_DATA_DIR=~/.nex-next/runner \
pnpm dev
```

The runner token is the server's `NEX_RUNNER_TOKEN`; both sides must agree. The
runner announces `workspacePath`s; the server routes browser calls to the owner
of each path, so **the same absolute path must not be served by two runners**
(the second is rejected). Model credentials come from the runner host's
`~/.pi/agent`, so each machine can use its own provider accounts.

Same stack, different ports (no dev.sh): the server accepts
`-addr 127.0.0.1:3031`, `-data-dir`, `-web-root` and the env vars documented in
`next/server/README.md`; the web client picks up `NEX_WEB_DEV_PORT` /
`NEX_WEB_DEV_SERVER_ORIGIN` in `packages/web/vite.config.ts`.

## Production-ish run

```sh
pnpm --filter @nex/web build
cd next/server
go build -o bin/nex-next-server ./cmd/nex-next-server
NEX_SERVER_AUTH_TOKEN=... NEX_RUNNER_TOKEN=... \
  ./bin/nex-next-server -addr :3030 -web-root ../../packages/web/dist -data-dir ~/.nex-next/server
```

Behind a reverse proxy that rewrites `Host`, set `NEX_ALLOWED_ORIGINS` to the
browser-facing host so the `/ws` same-Origin check passes (see
`next/server/README.md`). Runners point `NEX_SERVER_URL` at `wss://…/runner`.

## Tests

```sh
# server
(cd next/server && go test ./... -race -count=1 && go vet ./... && gofmt -l .)

# runner unit + in-process link tests
pnpm --filter @nex/next-runner test
RUN_PI_SMOKE=1 pnpm --filter @nex/next-runner test    # real pi model, uses ~/.pi/agent

# acceptance E2E against a running stack
next/dev.sh --e2e
# or against a stack you started yourself (workspace must contain notes.txt):
NEX_E2E_BASE_URL=http://localhost:5174 NEX_E2E_TOKEN=<browser token> node next/e2e/acceptance.mjs
```

`acceptance.mjs` prints a JSON summary and exits non-zero if any check fails
(model selected before send, no empty task rows, reply rendered, Read and Bash
tool cards, session in the sidebar after reload, transcript and model selection
after reopen). Screenshots and `summary.json` go to `next/e2e/out`
(`NEX_E2E_OUT` overrides). `NEX_E2E_CHROME` selects the browser; otherwise
Chrome/Chromium is auto-detected.

## Known gaps (MVP)

From [`docs/next/README.md`](../docs/next/README.md) and
[`packages/next-runner/README.md`](../packages/next-runner/README.md):

- Out of scope: terminals, file browser, git panel, permissions UI, sub-agents,
  port forwarding, SSH install, config push from the server, desktop app.
- The runner publishes **full snapshots only**; no deltas, and long sessions
  only show the tail window.
- `conversationRowsRangeV4` returns an empty page and `conversationPlansV4` is
  always empty.
- The sessions index is rebuilt from the runner's own metadata, so sessions
  created by running `pi` directly in the same workspace are not listed; titles
  are truncated first messages, not model-generated.
- No attachments, no permission/user-input interactions (pi uses its own tool
  policy).
- A workspace path must be unique across runners; multi-machine workspace
  identity (`remote:` registration) is post-MVP.
- The model catalog and API keys live on the runner; the server cannot push
  credentials yet.
- `OnboardingDialog` calls `useNexSessionService` conditionally in
  `packages/ui/src/hooks/useNexSessionService.ts`; the Rules-of-Hooks crash
  surfaced on web is a pre-existing UI bug, not fixed here. The acceptance E2E
  filters that console noise.
- No terminals/files/git/permissions/SSH install/config push yet.
