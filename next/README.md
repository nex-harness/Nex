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
| `next/e2e/cross-machine.mjs`  | Playwright E2E for the deployed topology: one server, two runners on different machines.                       |
| `next/deploy-remote.sh`       | Build + install the whole stack on a Linux host over SSH (see "Deploy to a Linux host").                       |

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

## Deploy to a Linux host

One command builds every artifact locally and installs them on a Linux box over
SSH. There are two layouts:

```sh
# Test box: everything under ~/nex-next, transient user units or setsid.
next/deploy-remote.sh <ssh-host> [--port 3041] [--build-on-host] [--no-start]

# Production: root-owned code in /opt/nex-next, user data in ~/.nex-next,
# two system units, port 80. Needs passwordless sudo on the host.
next/deploy-remote.sh <ssh-host> --system --port 80 \
  --workspace /absolute/workspace --reuse-token-from /path/to/existing/env
```

- `<ssh-host>` is anything `ssh` accepts (an `~/.ssh/config` entry works).
- The host needs Node 24, `npm`, `openssl`; `rsync` and `ssh` are needed
  locally. `--node <dir>` (or `NEX_DEPLOY_NODE_BIN`) picks the directory whose
  `node` is 24 when that is not the default (`--system` defaults to
  `~/.local/opt/node2414/bin`).
- `--port` defaults to 3041 in user mode and should be 80 in system mode;
  `--listen` defaults to `0.0.0.0`.
- `--workspace` is the runner workspace announced to the picker (user mode
  default `~/nex-next/ws`, system mode default `~/Developer/Nex`).
- `--reuse-token-from <file>` copies an existing `NEX_SERVER_AUTH_TOKEN` from a
  host file (for example the previous service's `EnvironmentFile`) instead of
  generating one. The value is copied on the host with Python and is never
  printed or sent over the wire.
- Tokens are otherwise generated on the host with `openssl rand -hex 24` and
  reused on redeploy — a redeploy never rotates tokens. `NEX_RUNNER_NAME` and
  `NEX_RUNNER_ID` fix the runner identity in the picker.

Artifacts it produces:

| Artifact | How it is built                                                                   | User mode path                   | System mode path                    |
| -------- | --------------------------------------------------------------------------------- | -------------------------------- | ----------------------------------- |
| Server   | `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build` (pure Go, static)                | `~/nex-next/bin/nex-next-server` | `/opt/nex-next/bin/nex-next-server` |
| Web      | `pnpm --filter @nex/web build`                                                    | `~/nex-next/web/`                | `/opt/nex-next/web/`                |
| Runner   | `pnpm --filter @nex/next-runner bundle` → `dist/runner.mjs` + `dist/package.json` | `~/nex-next/runner/`             | `/opt/nex-next/runner/`             |

`packages/next-runner/scripts/bundle.mjs` (esbuild, `platform=node`,
`format=esm`, `target=node24`) inlines workspace packages such as `@nex/shared`
and `ws`, and keeps `@earendil-works/pi-*` external so the host installs exactly
the pinned versions with `npm install --omit=dev`. The target machine does not
need the monorepo, pnpm, or a workspace link.

### User-mode layout (test box)

```
~/nex-next/
  bin/nex-next-server       # static Linux binary
  bin/start-server.sh       # supervisor-aware start (systemd-run or setsid)
  bin/start-runner.sh
  bin/stop-server.sh  bin/stop-runner.sh
  bin/start.sh  bin/stop.sh  bin/restart.sh
  web/                      # production web build served by the Go server
  runner/runner.mjs         # bundled runner + node_modules (pi SDK)
  data/server/              # server-id.json, web-sessions.json, settings.json
  data/runner/              # runner id, pi sessions, per-workspace metadata
  ws/notes.txt              # demo workspace ("hello from linux")
  logs/  secrets.env  port  listen  workspace.path  supervisor
```

Start and stop (the deployment picks `systemd-run --user` when user linger is
enabled, otherwise `setsid` + pid files; both survive SSH disconnects):

```sh
ssh <host> ~/nex-next/bin/stop-runner.sh    # stop only the runner
ssh <host> ~/nex-next/bin/start-runner.sh   # start it again
ssh <host> ~/nex-next/bin/restart.sh        # stop + start both
ssh <host> ~/nex-next/bin/stop.sh           # stop both
```

With the systemd supervisor the units are the transient user units
`nex-next-server.service` and `nex-next-runner.service`
(`systemctl --user status nex-next-runner.service`, `journalctl --user -u …`).
User linger is required for them to outlive the SSH session:

```sh
sudo loginctl enable-linger "$USER"   # once per host
loginctl show-user "$USER" -p Linger  # Linger=yes
```

With the `setsid` supervisor use the pid files:

```sh
kill "$(cat ~/nex-next/server.pid)"   # stop the server
kill "$(cat ~/nex-next/runner.pid)"   # stop the runner
tail -f ~/nex-next/logs/server.log ~/nex-next/logs/runner.log
```

Open `http://<host>:<port>/` and sign in with
`NEX_NEXT_BROWSER_TOKEN` from `~/nex-next/secrets.env`.

### System-mode layout (production)

```
/opt/nex-next/                    # root:root, 0755
  bin/nex-next-server             # current binary
  bin/nex-next-server.prev        # previous binary (quick rollback)
  web/  web.prev/                 # current / previous web build
  runner/  runner.prev/           # current / previous runner bundle
~/.nex-next/                      # owned by the service user
  env                             # NEX_SERVER_AUTH_TOKEN, NEX_RUNNER_TOKEN (600)
  server/                         # server-id.json, web-sessions.json, settings.json
  runner/                         # runner id, pi sessions, workspace metadata
<workspace>/                      # e.g. ~/Developer/Nex (runner cwd)
```

`nex-next-server.service` (`User=<deploy user>`, `EnvironmentFile=~/.nex-next/env`)
listens on `0.0.0.0:80` with `AmbientCapabilities=CAP_NET_BIND_SERVICE` and
`CapabilityBoundingSet=CAP_NET_BIND_SERVICE`; `Restart=on-failure`.
`nex-next-runner.service` runs the bundled `runner.mjs` with the host's Node 24
(`After=`/`Wants=` the server, not `PartOf=`, so restarting the server does not
stop the runner), `NEX_SERVER_URL=ws://127.0.0.1:80/runner`, `Restart=always`
and `RestartSec=3`. Both are `WantedBy=multi-user.target` and enabled by the
script.

```sh
sudo systemctl status nex-next-server nex-next-runner
sudo journalctl -u nex-next-runner -f
sudo systemctl restart nex-next-server     # runner keeps its connection attempt
sudo systemctl restart nex-next-runner
```

### Redeploy

Re-running the same command is idempotent. It rebuilds locally, installs the
binary through a staging dir, moves the previous `bin/nex-next-server`, `web/`
and `runner/` to `.prev`, keeps `~/.nex-next/env` and the data dirs untouched,
then restarts both units. The server is restarted first and the runner is only
restarted once the listener answers.

Quick rollback of one component without touching the service files:

```sh
sudo install -m 0755 /opt/nex-next/bin/nex-next-server.prev /opt/nex-next/bin/nex-next-server
sudo systemctl restart nex-next-server
# or: sudo mv /opt/nex-next/web.prev /opt/nex-next/web && sudo systemctl restart nex-next-server
```

### Reverting the cutover to an older stack

The cutover from a previous `nex-server` install backs that service up first:

```
/var/backups/nex-old-YYYYMMDD/
  nex-server.service  nex-server.service.d/  nex-server  rollback.sh
```

`rollback.sh` stops and disables the nex-next units, restores the unit,
drop-ins and binary, runs `daemon-reload` and starts `nex-server` again. It
never touches user data (`~/.nex`, `/var/lib/nex-server`, `~/.pi` are left
alone). Run it with `sudo /var/backups/nex-old-YYYYMMDD/rollback.sh`; it is
syntax-checked (`bash -n`) at creation time and is safe to re-run.

### Reverse proxy in front of the server

When a reverse proxy terminates TLS and forwards to the Go server:

- **WebSocket upgrade**: the proxy must pass `Upgrade`/`Connection` headers for
  both `/ws` (browser) and `/runner` (remote runners). `X-Forwarded-Proto: https`
  is what makes the login cookie `Secure`; `/ws` and `/runner` both work over
  `wss://` through a correct upgrade.
- **Origin check**: `/ws` compares the browser's `Origin` host with the request
  `Host`. A proxy that rewrites `Host` to the upstream address needs the
  browser-facing host in `NEX_ALLOWED_ORIGINS` (`--allowed-origins` flag or env;
  comma-separated host patterns). Add it only when the Host is actually
  rewritten; a plain proxy that preserves `Host` needs nothing.
- **Rate limiter**: `/api/auth/login` allows 5 failures per client IP per 60 s.
  Behind a proxy every client shares the proxy's IP, so five bad logins lock
  everyone out for a minute. There is intentionally no `X-Forwarded-For` trust:
  adding it without a trusted-proxy allowlist would let any client spoof the
  header and bypass the limit. A trusted-proxy allowlist is future work.
- **Web client assets** are served by the Go server with `index.html` fallback;
  no proxy rewrite rules for SPA routes are needed beyond sending everything to
  the server.

### Connecting runners on other machines

Each extra machine runs the same bundle and dials the server:

```sh
# on the remote machine, after copying runner/ from the deployment
cd ~/nex-next/runner
NEX_SERVER_URL=ws://<server-host>:<port>/runner \
NEX_RUNNER_TOKEN=<NEX_NEXT_RUNNER_TOKEN from secrets.env> \
NEX_RUNNER_NAME=<display name> \
NEX_RUNNER_WORKSPACES=/absolute/workspace/on/that/machine \
NEX_RUNNER_DATA_DIR=~/.nex-next/runner \
node runner.mjs
```

Through a TLS reverse proxy use `wss://<public-host>/runner`; `/runner`
disables the WebSocket Origin check (it authenticates with the bearer token),
so no `NEX_ALLOWED_ORIGINS` entry is needed for runners. The token is the one in
the server's secret file (`NEX_RUNNER_TOKEN`).

Model catalog and credentials come from that machine's `~/.pi/agent`, so every
runner can use its own provider accounts. The workspace path must be unique per
machine: two runners announcing the same absolute path are legal (both appear in
the picker with their machine name), but path-only routing treats it as
ambiguous and the UI always sends the `runner:` identity.

`next/e2e/cross-machine.mjs` exercises exactly this topology (two machines,
different workspaces, production web served by the Go server):

```sh
NEX_E2E_BASE_URL=http://<host>:<port> \
NEX_E2E_TOKEN=<browser token> \
NEX_E2E_LINUX_NAME=<linux runner name> \
NEX_E2E_MAC_NAME=<mac runner name> \
NEX_E2E_STOP_LINUX_CMD="ssh <host> '~/nex-next/bin/stop-runner.sh'" \
NEX_E2E_START_LINUX_CMD="ssh <host> '~/nex-next/bin/start-runner.sh'" \
node next/e2e/cross-machine.mjs
```

With `--system` the stop/start commands are
`ssh <host> 'sudo systemctl stop nex-next-runner'` /
`ssh <host> 'sudo systemctl start nex-next-runner'` (or the same without sudo
when the user may manage the unit). The E2E restores the runner on failure, but
check `systemctl status nex-next-runner` if the run is interrupted.

Without the stop/start commands the resilience section is reported as skipped.
`acceptance.mjs` and `permissions.mjs` accept the same arbitrary base URL;
`acceptance.mjs` takes `NEX_E2E_EXPECT_TEXT`, `NEX_E2E_ALLOW_EXISTING_TASKS=1`
and `NEX_E2E_WORKSPACE` for a stack that keeps earlier sessions, and
`permissions.mjs` takes `NEX_E2E_FILE_CHECK_CMD` (for example
`ssh <host> "cat {file} 2>/dev/null"`) when the workspace lives on the runner
host rather than the machine running the test.

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
