# Runner capabilities and self-update — spec

Status: follow-up to `runner-folders.md` on `feature/next`.

## Problems

1. **The UI cannot tell what a runner supports.** Every runner reports version `0.1.0`.
   A runner installed before `runner-workspaces` existed answered "Choose folder…" with
   `Unknown method runner-workspaces.add`.
2. **SSH-installed runners never update.** A server deploy updates the server and the
   server host's own runner, and restages the install bundles. Machines installed over
   SSH keep the code they were installed with, so every runner-side change needs a
   manual reinstall on each machine.

## Decisions

1. **Build identity.** `scripts/bundle.mjs` stamps every runner build with a `buildId`:
   the sha256 of `runner.mjs` + `pty-host.mjs` + `lib/ptyHostAssets.mjs`, first 16 hex
   characters. It is written to `dist/build.json` as `{buildId, version, builtAt}` and
   embedded in `runner.mjs`, so the running process knows its own id without reading
   files. The staging script copies `build.json` into every platform bundle.
   - The id is content-derived: identical code gives identical ids on every platform,
     and nothing has to be bumped by hand.
2. **Capabilities in `hello`.** The runner sends `buildId` and `capabilities: string[]`.
   This batch uses: `runner-workspaces`, `machine-scope`, `search`, `self-update`. The
   server stores both per runner and exposes them in `/api/server-info` workspace entries
   (`runnerBuildId`, `runnerCapabilities`) and in `GET /api/config/runners`. Missing
   fields mean "legacy runner, no optional features".
3. **The UI gates features on capabilities.** "Choose folder…" is enabled only when the
   machine reports `runner-workspaces`. Otherwise the button is shown disabled, with the
   tooltip "Update this machine to choose folders", next to an **Update** action
   (decision 5). The install wizard never shows a disabled state, because a fresh install
   always has the current build.
4. **The server knows the current build per platform.** On startup and on every
   `/api/config/runners` call (cheap, cached by file mtime), the server reads
   `<bundles>/<platform>/build.json`.
   - A runner whose `hello.buildId` differs from the bundle for its `os-arch` is
     **outdated**.
   - `GET /api/config/runners` adds `buildId`, `latestBuildId`, `outdated`, `capabilities`
     and `updateState` (`idle | downloading | restarting | failed`, plus `updateError`).
5. **Self-update protocol (pull over the existing link).**
   - Trigger:
     - **Automatic:** when an outdated runner that has `self-update` connects, the server
       sends `{t:"update", buildId}`.
     - **Manual:** `POST /api/runner-update?runnerId=<id>` sends the same frame.
     - The server sends at most one update frame per runner connection, so a runner that
       keeps failing cannot loop.
   - Download: the runner calls `GET /api/runner-bundle/<platform>/<file>` with its runner
     token (Bearer, the same token as the link). The server serves only the allow-listed
     files `runner.mjs`, `pty-host.mjs`, `lib/ptyHostAssets.mjs` and `build.json`, from
     the bundle dir for the runner's own platform.
     - `package.json` dependencies and the private Node are **not** updated. A bundle
       whose `package.json` dependency set differs from the installed one is reported as
       `updateError: "dependencies changed; reinstall required"`, and the runner does not
       swap.
   - Verify: the runner computes the buildId of the downloaded files and requires it to
     equal the frame's `buildId`. A mismatch fails the update.
   - Swap: files are written to `<install>/.update/<buildId>/`, then each target is
     renamed into place (`runner.mjs` last). The previous files go to
     `<install>/.prev/`. Renames are atomic on POSIX; on Windows a rename over a file
     that is not open succeeds, because the running `runner.mjs` is only read at start.
   - Restart: the runner reports `{t:"updateStatus", state:"restarting", buildId}`, closes
     the link and exits with code **75**. How it comes back depends on the supervisor:
     - systemd (`Restart=always`) and launchd (`KeepAlive`) restart it.
     - Windows (`start-runner.cmd`) and the `setsid` fallback have no supervisor. The
       installer now writes a wrapper loop that restarts the runner on exit code 75, and
       only on 75, so a crash does not loop.
     - Runners installed before this batch have no loop wrapper on Windows or setsid
       hosts. There the runner spawns a detached copy of itself before exiting.
   - Rollback: if the new build does not send `hello` within 60 s of the restart, the
     next start of the supervisor wrapper finds `.update-pending` (written before the
     swap, removed after the first successful `hello`) and restores `.prev/`. This
     rollback lives in the wrapper/loop, so it covers a new build that crashes on
     startup.
6. **The machine that hosts the server** (deploy-remote system units) is updated by
   `deploy-remote.sh` as today. It reports its `buildId`, and when it matches nothing is
   sent.

## Event order

```
deploy → server reads bundles/<platform>/build.json (latest)
runner hello {buildId:old, capabilities} → server: outdated
  server → runner {t:"update", buildId:new}                 (once per connection)
  runner: GET /api/runner-bundle/<platform>/* (Bearer runner token)
          verify buildId → write .update/<id> → .update-pending → swap (.prev kept)
  runner → server {t:"updateStatus", state:"restarting"}; exit 75
  supervisor/wrapper restarts runner.mjs (new)
  runner hello {buildId:new} → server: up to date; runner deletes .update-pending
UI: /api/config/runners + server-info reflect buildId/capabilities; Choose folder enabled
```

Owners:

- The server owns "what is latest", per platform, read from the bundle files.
- The runner owns its files and the swap.
- The supervisor or wrapper owns restart and rollback.
- The UI only reads state and triggers a manual update.

## Security

The bundle endpoint requires the runner token and serves fixed file names from a fixed
directory, so it allows no path input. The runner verifies content against the frame's
`buildId` before swapping. No browser credential can fetch runner code. The manual update
endpoint requires a browser session.

## Acceptance

- Runner unit:
  - The buildId is computed identically by bundle.mjs and by the runner.
  - The update downloads, verifies and swaps; it keeps `.prev`; it exits 75.
  - A wrong buildId aborts the update without swapping.
  - Changed dependencies abort the update with a reinstall error.
  - The hello carries `buildId` and `capabilities`.
- Server Go:
  - The bundle endpoint requires the runner token, allow-lists files, and refuses
    traversal.
  - `outdated` is computed per platform.
  - The update frame is sent once per connection.
  - `/api/config/runners` fields are present.
- UI unit: the picker disables "Choose folder…" without the capability and offers Update.
- Browser E2E `next/e2e/runner-update.mjs` (`--e2e=update`). The dev stack stages a
  bundle for the local platform whose `build.json` differs from the running runner's, and
  the runner runs under a test wrapper that restarts on exit 75. Checks:
  - The runner reports outdated.
  - It updates by itself and reconnects with the new buildId.
  - `/api/config/runners` shows up to date.
  - "Choose folder…" is enabled.
- Manual check: update the MacMini runner (launchd) from the live site.
