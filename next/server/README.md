# nex-next-server (Go)

Server for the `docs/next/README.md` MVP topology: the existing web client in
`packages/web` talks the binary Channel RPC protocol over `/ws`, and any number
of agent runners dial in over `/runner`. The server owns auth, the runner
registry, the workspace routing table, UI settings and event fan-out.

## Build and run

From `next/server`:

```sh
go build ./...
go build -o nex-next-server ./cmd/nex-next-server

NEX_SERVER_AUTH_TOKEN=browser-secret \
NEX_RUNNER_TOKEN=runner-secret \
NEX_WEB_STATIC_ROOT=../packages/web/dist \
./nex-next-server -addr :3030
```

The web root is optional. When it is missing the server logs a warning and
answers 404 for non-API paths, which is enough for the API/WebSocket tests.

## Configuration

| Flag               | Env                           | Default                               | Meaning                                                                                                  |
| ------------------ | ----------------------------- | ------------------------------------- | -------------------------------------------------------------------------------------------------------- |
| `-addr`            | `NEX_SERVER_ADDR` (or `PORT`) | `:3030`                               | Listen address; a bare port becomes `:PORT`                                                              |
| `-data-dir`        | `NEX_DATA_DIR`                | `~/.nex-next/server`                  | `server-id.json`, `web-sessions.json`, `settings.json`                                                   |
| `-web-root`        | `NEX_WEB_STATIC_ROOT`         | `packages/web/dist` (relative to cwd) | Built web client                                                                                         |
| `-name`            | `NEX_SERVER_NAME`             | hostname                              | Display name in `/api/server-info`                                                                       |
| `-allowed-origins` | `NEX_ALLOWED_ORIGINS`         | — (same host only)                    | Comma-separated extra `Origin` host patterns accepted by `/ws` (for reverse proxies that rewrite `Host`) |
| —                  | `NEX_SERVER_AUTH_TOKEN`       | **required**                          | Browser login token                                                                                      |
| —                  | `NEX_RUNNER_TOKEN`            | **required**                          | Bearer token for `GET /runner`                                                                           |

Tokens are never logged. Sessions are persisted as SHA-256 hashes of the
session id in `<data>/web-sessions.json`; rotating the token discards every
stored session (fingerprint mismatch).

## HTTP surface

| Route                   | Behaviour                                                                                                  |
| ----------------------- | ---------------------------------------------------------------------------------------------------------- |
| `GET /api/auth/session` | `{authRequired, authenticated}`                                                                            |
| `POST /api/auth/login`  | `{token}` → 200 + `nex_session` cookie; 400 malformed; 401 bad token; 429 after 5 failures per IP for 60 s |
| `POST /api/auth/logout` | revokes the session and clears the cookie                                                                  |
| `GET /api/server-info`  | `ServerRemoteInfo` (`protocolVersion:1`); workspaces come from connected runners                           |
| `GET /ws`               | browser Channel RPC (binary frames)                                                                        |
| `GET /runner`           | runner link (text JSON frames, bearer auth)                                                                |
| everything else         | static SPA with `index.html` fallback (unknown `/api/*` never falls back)                                  |

`?token=<auth token>` on a page request mints a session cookie; on protected
paths it authorizes the request without minting one, matching
`packages/server/src/webAuth.ts`.

`/ws` keeps the WebSocket library's same-Origin check: the session cookie is
`SameSite=Lax`, which does not stop a cross-site WebSocket handshake, so
accepting any Origin would allow cross-site WebSocket hijacking. Deployments
whose proxy rewrites `Host` must list the browser-facing host in
`NEX_ALLOWED_ORIGINS`. `/runner` disables the check because it authenticates
with a bearer token and its clients are not browsers.

Backpressure: each browser connection has a bounded outbound queue (1024
frames / 64 MiB). On overflow the server marks it closed immediately and tears
it down in the background (sending `unlisten` to runners), so one slow browser
never blocks a runner's event pump or another browser. Frames larger than
32 MiB close the connection; `/runner` reads are limited to 32 MiB per frame.

## Package map

| Package               | Responsibility                                                                |
| --------------------- | ----------------------------------------------------------------------------- |
| `cmd/nex-next-server` | Flag/env parsing, HTTP listener, graceful shutdown                            |
| `internal/app`        | Assembly: local channel registry, runner registry, handlers, stable server id |
| `internal/config`     | Config resolution and validation                                              |
| `internal/auth`       | Token comparison, hashed session store, login rate limiter                    |
| `internal/httpapi`    | Auth middleware, `/api/*`, static SPA                                         |
| `internal/browserws`  | `/ws`: rpcwire frame dispatch, outbound queue, keepalive                      |
| `internal/runners`    | Runner set, `workspacePath → runnerId` routing table, re-listen bindings      |
| `internal/runnerws`   | `/runner` link protocol (hello/welcome, call/result, listen/event)            |
| `internal/router`     | Pure routing table: method → local / workspace / first runner                 |
| `internal/local`      | Server-local channels (`setting`, `broadcast`, stubs, local nex-agent table)  |
| `internal/rpcwire`    | Wire format and JSON bridge (see its README)                                  |

## Routing table (browser → server)

All forwarded calls route by the top-level `workspacePath` in the argument.
The browser sends Channel RPC argument arrays (`[arg]`), so the router also
looks inside the array. `workspaceIdentity` is never read and never set: the UI
treats any non-empty identity as a remote workspace.

| Channel                                                                                                                             | Method(s)                                                                                                                                                                                | Destination                                                        | Routing field                     |
| ----------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------ | --------------------------------- |
| `setting`                                                                                                                           | `get`, `update`, `updateDataBaseDir`, `ensureDefaultProject`                                                                                                                             | local                                                              | —                                 |
| `broadcast`                                                                                                                         | `send`, claim methods                                                                                                                                                                    | local (fan-out to every `onMessage`)                               | —                                 |
| `system`, `git`, `mcp-sync`, `coding-plan-subscription`, `settings-sync`, `subagents`, `client-scenes`, `file`, `window-controller` | fixed tables in `internal/local/stubs.go`                                                                                                                                                | local                                                              | —                                 |
| `nex-agent`                                                                                                                         | `helloConversationV4`, `initializeConversationV4`, `syncAppRuntimePreferences`, `listAllAutomations`, `hasActiveCuaOperationTurn`                                                        | local (`internal/local/nexagent.go`)                               | —                                 |
| `nex-agent`                                                                                                                         | v4 conversation/sessions-index/workspace-config methods, `sendConversationCommandV4`, rows/plans/attachments, legacy session/plugin/automation methods (see `internal/router/router.go`) | owning runner                                                      | `workspacePath`                   |
| `nex-agent`                                                                                                                         | `onDynamicConversationFrame`, `onDynamicSessionsIndexFrame`, `onDynamicWorkspaceConfigFrame`, …                                                                                          | owning runner                                                      | `workspacePath` in the listen arg |
| `nex-task`                                                                                                                          | `listTasks`, `listPinnedTasks`, `listArchivedTasks`, `listDeletedTaskIds`, `onDynamicWorkspaceEvent`                                                                                     | owning runner                                                      | `workspacePath`                   |
| `nex-task`                                                                                                                          | `listPinnedTaskIds`                                                                                                                                                                      | first connected runner                                             | —                                 |
| `nex-session`                                                                                                                       | `readWorkspacePresentation` and other workspace methods                                                                                                                                  | owning runner                                                      | `workspacePath`                   |
| `model-selection`, `provider-settings`                                                                                              | `getView` and `onDidChange`                                                                                                                                                              | first connected runner                                             | —                                 |
| unknown channel                                                                                                                     | any                                                                                                                                                                                      | `PromiseError "Channel name '<x>' not found"`; listens stay silent |

Error mapping to the browser:

- runner result `ok:false` → `PromiseError` with the runner's message/name;
- runner disconnect with pending calls → `PromiseError "runner offline"`;
- no runner for a path → `"no runner connected for workspace"`;
- no runner at all (`model-selection`) → `"no runner connected"`;
- unknown method on a known local channel → `"Method not found: <name>"`;
- unknown event on a known channel → empty event (never fires).

## Runner link behaviour

- Duplicate `runnerId`: newest connection wins; the old socket is closed.
- Duplicate `workspacePath` from another runner: the new workspace is rejected
  with a warning; the first owner keeps routing.
- On disconnect: pending calls fail with `runner offline`, browser listen specs
  are kept (but stop receiving events), and the routed paths disappear from
  `/api/server-info` / `setting.get().recentProjects`.
- On reconnect: the server re-issues every `listen` belonging to that runner
  with the same listen id, so browser streams survive runner restarts.

## Tests

```sh
go vet ./...
go test ./... -race -count=1
gofmt -l .
```

| Test                                | Covers                                                                                                                                                                                  |
| ----------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `internal/httpapi/httpapi_test.go`  | login ok/bad/malformed/lockout, cookie attributes incl. `Secure` behind `X-Forwarded-Proto`, protected paths, `?token=` cookie minting, logout, server-info shape, static/SPA/traversal |
| `internal/local/local_test.go`      | settings defaults merge, atomic persistence, routed `recentProjects`, broadcast fan-out/claims, window-controller snapshot frame                                                        |
| `internal/router/router_test.go`    | routing table incl. the client's `[arg]` call envelope                                                                                                                                  |
| `internal/runners/registry_test.go` | path routing, duplicate path/runner handling, re-listen on reconnect, change subscriptions                                                                                              |
| `internal/config/config_test.go`    | required tokens, flag/env precedence, defaults                                                                                                                                          |
| `test/e2e_test.go`                  | fake runner + fake browser: forwarded call/result, dynamic listen/event, pending call fails on runner death, re-listen on reconnect, local channels, duplicate runner/path handling     |

TS cross-check against the real `@nex/client` (run from the repo root, Node 24):

```sh
PATH=<node-24-bin>:$PATH \
NEX_SERVER_URL=http://127.0.0.1:3030 \
NEX_SERVER_AUTH_TOKEN=browser-secret \
node_modules/.bin/tsx next/server/test/ts-client-smoke.ts
```
