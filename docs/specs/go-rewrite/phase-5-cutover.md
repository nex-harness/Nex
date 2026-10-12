# Phase 5 — Cutover: Go becomes the default

Part of [the Go rewrite plan](README.md). Depends on [Phase 3](phase-3-services.md) and [Phase 4](phase-4-agent-breadth.md).

## Goal

- The server distribution (tarball + `install.sh` + systemd/launchd unit, and the Docker image) ships the **Go server and Go agent** by default.
- The Desktop app ships the **Go agent** (and, if decision D10 = option B, a Go local host).
- Remote workspace deployment (SSH / WSL / Docker) deploys the **Go agent binary**.
- Install, update, uninstall, crash-restart and status commands behave as today.
- A Node-based fallback is still shipped for exactly one release, then removal is a separate, explicitly approved change (step 5.11).

## Non-goals

No new features. No deletion of the TypeScript backend in this phase (only after step 5.11's approval). No UI changes (except Desktop wiring if D10 = B).

## Entry criteria

`gate.sh 3` and `gate.sh 4` exit 0, and the dual-run canary (step 5.1) has been in use for at least 2 weeks without a P1 defect.

## Fact discovered while writing this plan — it changes scope

The Desktop app does **not** use `nex-server`. Each Desktop window runs a Node `utilityProcess` ("host", `packages/desktop/src/host/index.ts`) that calls
`createLocalServices(...)` **in-process** (line ~2386) and talks to the Renderer over a MessagePort (`MessagePortProtocol`, `ChannelServer`). The host also handles
Electron-only callbacks (`browserControlMainBridge`, `parentPort.postMessage` for session send requests, provider provisioning changes, cua reporters, `createFullLogArchive` via main).
Remote workspaces use `windowRemoteConnectionRegistry.ts` (786 lines) and `remoteWorkspaceServiceCollection.ts` (298 lines) inside the host.

Consequences:

- Phases 2–3 (Go server + Go services) benefit **Web, the standalone server and Docker** directly. Desktop benefits only from the **Go agent** (Phase 1/4) unless D10 = B.
- Replacing the Desktop's local services with Go is a separate, UI-adjacent project (Desktop main/host changes), so it is a **decision**, not an assumption.

## Decision D10 — Desktop local host (resolve in step 5.2)

| Option                                                                        | What changes                                                                                                                                                                                                                                                                    | Cost               | Risk                                                                                                                                           |
| ----------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------ | ---------------------------------------------------------------------------------------------------------------------------------------------- |
| **A (default)** Keep the Node host in Desktop; Desktop uses the Go agent only | Desktop packaging bundles `nex-agent` (Go) instead of `nex.cjs`; `NEX_AGENT_RUNTIME.nodeBundleEntryFile`/`resolveNodeBundleSegments` usage replaced by native binary path (`binaryKind` is already `"native-binary"`)                                                           | Low (days–weeks)   | Desktop still ships Node (Electron's) — acceptable, it already does                                                                            |
| **B** Desktop spawns a Go local host and the Renderer attaches to it          | New transport (loopback WebSocket `/ws/host` or inherited-fd socket) replacing MessagePort; port Electron-callback surfaces (browser control, native dialogs, log archive via main, automation dispatch) to RPC calls main↔Go; `windowRemoteConnectionRegistry` semantics in Go | Very high (months) | Touches Desktop main/host code owned by the UI team; `AGENTS.md`: one window-scoped Local Host per window, remote workspaces managed in-window |
| **C** Desktop connects to a shared per-user Go server                         | Violates window-scoped host rule                                                                                                                                                                                                                                                | —                  | Not recommended                                                                                                                                |

Everything below assumes **A**. If B is chosen, add `phase-6-desktop-host.md`; do not fold it into this phase.

## Steps

### 5.1 Dual-run canary and fallback switch

- **Source:** `packages/nex-server-cli/src/runtime/{releaseManager,stableLauncher,manifest}.ts`, `packaging/install.sh`, `.github/workflows/release-server.yaml`.
- **Target:** `docs/specs/go-rewrite/phase-5-canary.md`; release channel naming (`alpha-<shortsha>` rolling builds already exist per `release-server.yaml`).
- **Do:** Publish Go-based server builds on the **alpha** channel (`nex-server-go-<target>-alpha-<shortsha>.tar.gz`) next to the Node ones. Dogfood group runs Go on alpha. Define a defect severity scale (P1 = data loss, crash loop, auth bypass, cannot start; P2 = feature broken with workaround; P3 = cosmetic). Define the fallback command: reinstalling the Node tarball over the same data dir must work (tested in 5.8).
- **Tests:** none new; this step is process. The entry criterion "2 weeks, no P1" is tracked in `phase-5-canary.md` with dates and defect links.
- **Done when:** the canary log shows ≥ 14 days, ≥ N dogfood users (set N in the doc), zero open P1.

### 5.2 Decisions and configuration of the final shape

- **Target:** `decisions.md` updates.
- **Resolve:** D10 (above); where the Node sidecar runtime comes from (D4 consequence) — note a discrepancy to settle: `packages/nex-server-cli/src/contracts.ts` declares `SERVER_RUNTIME_NODE_VERSION = "22.16.0"` while `mise.toml` pins Node `24.14.0` and `release-server.yaml` says the build must match the `third-party/runtime/sources.json` entry (24.14.0). Decide the single Node version for sidecars and update `THIRD-PARTY-NOTICES.md`/`third-party/runtime/sources.json` accordingly (license obligations: `NOTICE.md`, `scripts/generate-third-party-notices.mjs`);
  versioning source: `NEX_VERSION` is injected at build time by tsup from the **root `package.json` `version`** (`packages/server/tsup.config.ts`: `__NEX_VERSION__`; the observed value is `1.2.3`). Go must take the same value via `-ldflags "-X"`,
  produced by a build script that reads the root `package.json`, so there is a single version source.
- **Done when:** no `TBD` in `decisions.md` (`G0.2` pattern re-run).

### 5.3 Build and packaging of the Go server

- **Source:** `packages/server/scripts/build-sea.mjs`, `sea-pty-assets.mjs`, `packages/server/packaging/{install.sh,nex-server.service,com.nex.server.plist,README.md}`, `packages/server/build-http-bundle.ts`, `scripts/stage-docker-context.sh`, `scripts/build-native-search-tools.mjs`, `package-native-search-tools.mjs`, `apps/nex-cli/dependencies/native-search` (ripgrep/bfs/ugrep + `SHA256SUMS`), `scripts/builtin-provider-config.mjs`.
- **Target:** `scripts/go-rewrite/build-release.sh <target>`; `go/cmd/nex-server` embeds: builtin provider config (`//go:embed` of the artifact produced by `builtin-provider-config.mjs`), default prompts, migrations. Output tarball:
  ```
  nex-server-<target>-<version|shortsha>.tar.gz
    nex-server[.exe]            (Go; also acts as agent when invoked `app-server --stdio`? see decision below)
    nex-agent[.exe]             (Go agent; or same binary with subcommand)
    sidecars/                   (JS bundles, D4)  +  runtime/node[.exe] (if sidecars shipped)
    native-search/              (ripgrep, bfs, ugrep with SHA256SUMS — existing tooling)
    web/                        (packages/web/dist)
    packaging/                  (install.sh, systemd unit, launchd plist)
  ```
- **Decision inside this step:** one multi-call binary (`nex-server app-server --stdio` as the agent) vs two binaries. Prefer **one binary** (simpler update atomicity, smaller total size); the host's `NEX_AGENT_RUNTIME.spawnArgs = ["app-server","--stdio"]` already uses a subcommand shape, and `resolveEntrySegments` names the file `nex-agent` — so ship `nex-agent` as a hard link/copy or have the resolver accept the server binary. Record in `decisions.md`.
- **Do:** Targets: `linux-x64, linux-arm64, darwin-arm64, darwin-x64, win32-x64` (existing release workflow publishes `darwin-arm64, linux-x64, linux-arm64` for the server; Windows/macOS-x64 server artifacts exist only if `build-sea.mjs` supports them — read it; do not claim new targets without a decision). Reproducible builds: `-trimpath`, fixed `-buildvcs=false`, `SOURCE_DATE_EPOCH`, deterministic tar (`scripts/deterministic-tar-archive.mjs` exists — reuse it or port). `CGO_ENABLED=0` for all targets (pure-Go SQLite, D3). Strip: `-ldflags "-s -w"`. Report binary size.
  Windows: `.exe` + manifest; macOS: codesign + notarization for any binary shipped inside signed app bundles (Desktop) — the Desktop `entitlements*.plist` live in `packages/desktop/build`; a child binary needs inheriting entitlements (`entitlements.mac.inherit.plist`). Verify with `scripts/doctor-macos-release-app.sh`.
- **Tests:**
  - `build-release.sh` run twice → identical sha256 (reproducibility).
  - Tarball content test: expected file list and modes; no secrets (`scrub-check.mjs` over the tree); license files present (`THIRD-PARTY-NOTICES.md`).
  - `ldd`/`otool -L`/`dumpbin` check: Linux binary is statically linked (no libc dependency → works on `debian:bookworm-slim` and Alpine); macOS binary links only system libraries.
  - Smoke: extract, run `nex-server --version` equals the TS `NEX_VERSION`.
  - Size budget: record binary size; fail the build if > agreed budget (set after first build; compare with the SEA binary size which is ~Node-sized — measure it from the last Node release, do not guess).
- **Done when:** artifacts for all targets built in CI, reproducible, and smoke-tested on their native OS runners.

### 5.4 Replace `nex-server-cli` (installer, supervisor, update, service manager)

- **Source:** `packages/nex-server-cli/src/**` (42 files, 5.8k lines): `cli.ts`, `main.ts`, `contracts.ts` (protocol v1, crash window 5 min, backoff `[1,2,4,8,16] s`, `MAX_CONTROL_FRAME_BYTES` 64 KiB, lifecycle states
  `starting ready stopping stop-failed stopped crashed crash-loop-stopped updating uninstalling uninstalled`), `ipc/{controlServer,controlClient,controlError,framing}.ts`, `supervisor/{supervisor,crashBudget}.ts`,
  `platform/serviceManager.ts` (launchd / systemd / Task Scheduler descriptors), `runtime/*` (release download with sha256 manifest, immutable release tree hash `.release-integrity.json`, installer, update preparation/readiness — blocks update while tasks are running —, lock, stable launcher `nex`/`nex.cmd`, startup recovery, uninstall guard, installation ownership, shutdown wait), `packaging/*` (staging a distribution with a Node dist mirror), `server-core/*` (taskActivityTracker, parentDisconnect, serverIdentity).
- **Target:** `go/cmd/nex` (the user-facing `nex` command: `serve`, `status`, `stop`, `update`, `uninstall`, `install-service` … — **derive the exact command list from `cli.ts`**), `go/internal/ops/{supervisor,control,release,service,paths}/`.
- **Do:**
  - Keep the **control protocol** (framing, schema, error codes) byte-compatible so a Node-era `nex status` talks to a Go server and vice versa during the migration (`controlClient`/`controlServer` tests as vectors).
  - Paths layout (`runtime/paths.ts` `ServerLayout`: `serverRoot`, `stableBinDir`, `serviceDir`, `stablePathId(serverRoot)` hash for service names like `com.zhipu.nex.server.<id>`) — **identical**, so an existing install is found and upgraded in place; the service unit names must not change or the old unit would keep running.
  - Release download: manifest schema (`manifest.ts`: per-target `sha256`), HTTPS client via shared adapter (proxy/CA), resumable? (read `releaseDownload.ts`), verify sha256 **before** extraction, extract with zip-slip/tar-slip protection, immutable release directory + atomic switch (`current` symlink/junction; Windows uses junction or marker — read `immutableRelease.ts`/`stableLauncher.ts`), keep previous release for rollback, content-tree hash verification at startup.
  - Update flow: `updatePreparation` blocks when tasks are running (`runningTaskCount` from `taskActivityTracker`), `updateReadiness`, shutdown wait, restart, health check, automatic rollback if the new version fails health within the window (read `updateCommand.ts` for the exact policy; port it, don't invent).
  - Supervisor: crash budget identical constants; crash-loop-stopped state; status snapshot file; `parentDisconnect`.
  - Service manager: launchd plist, systemd unit (user vs system?), Windows Task Scheduler JSON descriptor — generate the same content (golden-file tests against TS output).
- **Tests:**
  - Golden tests for service descriptors on all three platforms (generated by running the TS `createDaemonServiceDescriptor` for fixture layouts).
  - Control-protocol cross-compat: TS client ↔ Go server and Go client ↔ TS server for every command, in CI (needs Node).
  - Crash-budget table with fake clock; supervisor test with a fake child that crashes N times.
  - Update tests in a temp root using a local HTTP server serving a fake manifest + tarballs: success; corrupted sha256 → abort, old version still runs; network drop mid-download → resumable or clean retry; running task → blocked; new version fails health → rolled back; concurrent `nex update` → lock prevents the second; power-loss simulation (kill -9 between extract and switch) → `startupRecovery` completes or reverts.
  - Uninstall guard tests (foreign installation ownership: refuse to remove what we do not own — `installationOwnership.ts`).
  - Windows-specific: junction creation, file locking of running `.exe` during update (rename trick), Task Scheduler registration (needs elevated runner; if not available in CI, document as manual test with a checklist).
- **Done when:** `nex` commands pass the above on Linux and macOS in CI; Windows pass or have a recorded manual run.

### 5.5 `install.sh` and in-place migration from the Node install

- **Source:** `packages/server/packaging/install.sh`, `nex-server.service`, `com.nex.server.plist`, `packaging/README.md`, CHANGELOG entries for server install.
- **Target:** updated `install.sh` (same URL shape and flags), `docs/specs/go-rewrite/phase-5-migration.md`.
- **Do:** Existing users on the Node server run `nex update` (Node-era CLI) → receives the Go release as an ordinary update (manifest points to Go tarballs) → service restarts with the Go binary using the same data dir, same service unit name, same port/host/token/workspace configuration source. `install.sh` fresh install also gets Go. Define the compatibility checks the **Node-era CLI** performs before applying an update (it validates manifest schema: Go release manifest must be accepted by the _old_ `serverRuntimeManifestSchema` — read `runtime/manifest.ts` and generate the manifest accordingly; if the old schema requires a Node runtime entry (`SERVER_RUNTIME_NODE_VERSION`) include one (the sidecar runtime) so old CLIs accept it).
- **Tests:** a matrix job: install each of the last 3 published Node releases (download from GitHub Releases in a throwaway root; offline mirror in CI cache), create sessions with the Node server, run `nex update` to the Go build, assert: service name unchanged, process is the Go binary, sessions/settings/provider config/web-sessions intact (`PRAGMA integrity_check`, same row counts), login cookie still valid, scheduled automations still fire (cron fixture), then downgrade (`nex update --to <node version>` or reinstall) and assert data still opens. Run on Linux (systemd in a container with systemd, or `systemd-nspawn`) and macOS (launchd).
- **Done when:** the matrix passes for 3 previous versions on Linux; macOS verified in CI or manual log.

### 5.6 Remote deployment (SSH / WSL / Docker) in Go

- **Source:** `packages/server/src/remote/*` (≈40 files): `backend.ts`, `ssh-backend.ts` (`ssh2`), `sshAuth.ts`, `sshUploadProgress.ts`, `wsl-backend.ts`, `wsl-detect.ts`, `wslProxy.ts`, `docker-backend.ts`, `docker-detect.ts`, `detectEnv.ts`, `deploy.ts`, `deployShared.ts`, `nexAgentDeploy.ts`, `nexAgentBundleWrapper.ts`, `nexAgentWrapperDeploy.ts`, `nexAgentDevDeploy.ts`, `nexAgentOfficialPlugin*.ts`, `runtimeToolDeploy.ts`, `serverBundleDeployCheck.ts`, `remoteAsset*.ts` (cache, CDN, installer, preflight, live identity, network, deploy decision), `remotePlatformSupport.ts`, `remoteDeployLock.ts`, `handshake.ts`, `stdio-socket.ts`, `connect.ts`, `create-backend.ts`, `localTarGz.ts`, `posixShell.ts`; shared `remoteTarget.ts`, `remoteSshHostKey.ts`, `remoteResourcePackages.ts`, `remoteAssetInstallMode.ts`, `remote-workspace-identity.ts`, `server-remote.ts`; HTTP `/api/connect-remote`, WS `/ws/remote/:id`; scripts `packages/nex-server-cli/scripts/verify-remote-ssh*.mjs`.
- **Target:** `go/internal/remote/{backend,ssh,wsl,docker,deploy,assets,handshake}/`, `go/internal/server/remoteroutes.go`.
- **Do:**
  - SSH: `golang.org/x/crypto/ssh` + `known_hosts` verification (`remoteSshHostKey.ts` rules: trust-on-first-use? read), agent forwarding/keys/password/passphrase flows in `sshAuth.ts`, SFTP or `cat >` streaming upload with progress, keepalive, proxy jump (`sshConfigAlias.ts` handles `~/.ssh/config`; shared with 3.2).
  - Deploy: now simply "upload the static `nex-agent` for the detected `platformArch`" (no Node bundle wrapper, no `nexAgentBundleWrapper`/`wrapper` logic) + native-search tools + official plugin assets; identity check (`remoteAssetLiveIdentity.ts`: skip deploy when the same identity is already installed), deploy lock, permission repair of legacy plugin directories (keep: remote hosts have old installs).
  - Platform support matrix: `remotePlatformSupport.ts` — keep the same accepted OS/arch/libc combos; static Go binaries remove the glibc requirement (document as improvement; don't remove checks that protect other components).
  - Handshake/stdio socket: remote agent is reached through `ssh ... nex-agent app-server --stdio` multiplexed as the stdio transport; the Go host speaks to it with the same process-manager abstraction as 3.10.1 (transport = ssh channel instead of child stdio). Workspace identity strings for remote workspaces come from the existing builder/parser (`remote-workspace-identity.ts`) — port into `go/internal/workspace/identity.go` with vectors from TS (do **not** hand-format).
  - Keep wire compatibility with existing remote hosts: a remote machine that still has the **Node** agent deployed must keep working with the Go host until redeployed (protocol unchanged → should hold; test it).
- **Tests:**
  - Unit: backend interface with fake transports; deploy decision table (identity same/different/missing, forced, dev mode); upload with network cut → resume/cleanup; lock contention.
  - Integration (CI containers): `docker run` an `openssh-server` container (Linux x64 and arm64 via QEMU if available) → deploy → run a session through the Go host → verify the process on the remote is the Go agent, tools execute remotely, files edited remotely; `known_hosts` mismatch → refused; wrong key → typed error the UI understands (error codes from `packages/shared/src/remoteTarget.ts`/server-remote).
  - WSL: Windows runner with WSL if available; else manual checklist.
  - Docker backend: container exec path.
  - Compatibility: Go host ↔ remote with Node agent (older install) — S01–S03 pass; Node host ↔ remote with Go agent (the reverse, relevant for Desktop option A): S01–S03 pass.
  - `verify-remote-ssh*.mjs` scripts reused unchanged against the Go server.
- **Done when:** integration matrix passes; compatibility cross-tests pass.

### 5.7 Docker image and compose

- **Source:** `Dockerfile`, `docker-compose.yml`, `docs/specs/docker-single-image.md` (product rules still hold: one image `ghcr.io/tylinux/nex-server`, port 3030, `/data`, `/workspace`, healthcheck `curl -fsS http://127.0.0.1:${PORT}/api/auth/session`, runtime deps `git openssh-client ca-certificates curl`), `.github/workflows/docker.yaml`, `scripts/stage-docker-context.sh`.
- **Target:** updated `Dockerfile`/staging script/workflow; spec addendum `docs/specs/docker-single-image.md` "Go server" section (spec before code, per `AGENTS.md`).
- **Do:** Context `dist-docker/` now contains `nex-server` (Go), `web/`, `sidecars/` + node runtime (if D4 sidecar) — Node runtime inside the image only if sidecars ship. Base image stays `debian:bookworm-slim` (git/ssh needed); a distroless/alpine variant is out of scope. Same env vars, same volumes, same healthcheck.
- **Tests:** the 7 acceptance items of `docker-single-image.md`; image size recorded vs previous release image (pull previous tag; report both); container restart keeps sessions; run as root as before (state owner `nex-data` volume permissions unchanged); `docker compose config` passes; multi-arch build `linux/amd64` (CI) and `linux/arm64` (local with QEMU, documented).
- **Done when:** acceptance passes, size report committed in the PR description.

### 5.8 Compatibility, upgrade/downgrade, data-safety suite

- **Target:** `scripts/go-rewrite/compat-matrix.sh`.
- **Do:** For every pair (Node-server N, Go-server G) × (Node-agent, Go-agent) × (Desktop host Node, Go agent) on a shared data dir:
  1. Create data with X, open with Y, mutate, reopen with X.
  2. Compare: row counts per table, `PRAGMA integrity_check`, settings JSON equality, provider config, memory/skills dirs tree hash, `web-sessions.json`.
  3. Concurrent run (Node + Go processes using the same dir simultaneously for 60 s with writes from both): no corruption, no `database is locked` failures beyond the busy timeout, WAL checkpoints succeed.
- **Tests:** the matrix itself; fault injection (disk full via tmpfs size limit, kill -9 during write, read-only dir) → typed errors, no partial files (atomic write verification by directory listing for temp leftovers).
- **Done when:** the whole matrix is green and attached to the release.

### 5.9 Performance and resource benchmarks

- **Do:** Measure Node vs Go with a fixed workload script (`scripts/go-rewrite/bench.sh`): cold start to `ready`; time to first token with the mock model; idle RSS; RSS with 10 sessions/50 sessions; 100 MB file read/grep; 1 000-message session open; 20 parallel terminals; install size; update download size. Record in `docs/specs/go-rewrite/benchmarks.md` (table, machine description, commit).
- **Tests/Done when:** no metric regresses by more than the thresholds in `benchmarks.md` (set before running; defaults: start-up ≤ Node, idle RSS ≤ Node, first-token latency within +50 ms, session open within +20%); regressions need a written justification.

### 5.10 Security review

- **Do:** Checklist with evidence (command output or test id) for: auth bypass attempts (path tricks `/api%2f…`, double slashes, case, `/ws` variants, HTTP method override) against the Go front; cookie flags; token comparison constant-time; rate limit; request size limits; WebSocket origin policy (what does Node do? replicate — `Origin` checking is absent in the TS read; decide and record); static traversal (done in 2.5); archive extraction (3.7, 5.4); update integrity (sha256 + immutable tree hash); sidecar isolation (4.12); egress policy (4.1); secrets redaction in logs (grep tests); file permissions of `web-sessions.json`, credentials (0600); dependency audit `govulncheck ./...` clean; `gosec` run reviewed; fuzz corpora committed; SBOM generation (`go version -m`, `cyclonedx-gomod`).
- **Done when:** `phase-5-security.md` lists every item with evidence; `govulncheck` has no reachable vulnerabilities.

### 5.11 Retire the Node backend (**separate approval required**)

- **Not done in this phase.** After one full release with Go as default and Node as the documented fallback, open a proposal (`phase-5-retire-node.md`) listing what would be deleted, what Desktop still needs (D10=A keeps `packages/services` + `packages/desktop/host` + `packages/rpc` + `packages/shared` for the Desktop local host!), and the architecture-policy/`knip`/`typecheck` script updates (root `package.json` `typecheck` project list; `architecture-policy.yaml` modules; `pnpm-workspace.yaml`). `AGENTS.md` rule: remove instruction/skill references when functionality is removed.
- **Done when:** (for this phase) the proposal exists and is reviewed; nothing is deleted.

### 5.12 Documentation and release

- **Do:** Update `README.md`, `README.zh-CN.md`, `AGENTS.md` (commands table: Go build/test/lint, gate script, where Go code lives; keep the TS commands that still apply), `apps/nex-cli/AGENTS.md` (pointer), `docs/specs/docker-single-image.md`, `packages/server/packaging/README.md`, `CHANGELOG.md` entry (breaking-change notes: install layout, any env-var behavior, removal of Node requirement on servers unless sidecars), release notes with upgrade and downgrade instructions. `docs/native-apple-clients-plan.md` mentions the Node SEA helper and `nex-server-cli` daemon — update to say the daemon is now Go (it recommended managing the daemon, which keeps working).
- **Tests:** docs link check (`scripts/go-rewrite/check-links.mjs` over the changed docs); every command in the docs is executed by `scripts/go-rewrite/doc-commands.sh` in a clean checkout (copy-paste test).
- **Done when:** link check and doc-command script pass; release notes reviewed.

### 5.13 Final acceptance run

- **Do:** On clean machines (Linux x64 VM, macOS arm64, Windows 11): fresh install from the release artifact → full UI checklist (Web + Desktop with Go agent) from `phase-5-acceptance.md` (the union of the checklists in `phase-1-usage.md`, `phase-3-usage.md`, `phase-4-usage.md`) → update from previous Node release → downgrade → uninstall (data kept/removed per prompt). Screenshots and logs archived with the release.
- **Done when:** all checklist items ticked with operator/date; artifacts archived.

## Exit gate — `scripts/go-rewrite/gate.sh 5`

| Check | Command                                                                                            | Pass condition                                                                                      |
| ----- | -------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------- |
| G5.0  | common `GN.0` block                                                                                | exit 0 (or baseline-listed failures only)                                                           |
| G5.1  | `gate.sh 3 && gate.sh 4`                                                                           | exit 0                                                                                              |
| G5.2  | `node scripts/go-rewrite/canary-status.mjs`                                                        | ≥ 14 days canary, 0 open P1 (reads `phase-5-canary.md`)                                             |
| G5.3  | `! grep -n 'TBD' docs/specs/go-rewrite/decisions.md`                                               | no undecided rows (D10 resolved)                                                                    |
| G5.4  | `bash scripts/go-rewrite/build-release.sh --all && bash scripts/go-rewrite/verify-reproducible.sh` | all targets built twice with identical sha256                                                       |
| G5.5  | `bash scripts/go-rewrite/verify-tarball.sh`                                                        | file list, modes, licenses, no secrets, static linking, `--version` == `NEX_VERSION`                |
| G5.6  | `go test ./internal/ops/... ./cmd/nex/...` + `node scripts/go-rewrite/control-compat.mjs`          | control protocol compatible both directions; golden service descriptors equal                       |
| G5.7  | `bash scripts/go-rewrite/update-matrix.sh --previous 3`                                            | in-place update from the last 3 Node releases OK; downgrade OK; data intact                         |
| G5.8  | `bash scripts/go-rewrite/remote-matrix.sh`                                                         | SSH (and Docker) deploy of Go agent works; cross-compat Node↔Go both ways S01–S03                   |
| G5.9  | `bash scripts/go-rewrite/docker-accept.sh --front go --full`                                       | 7 acceptance items pass; size report produced                                                       |
| G5.10 | `bash scripts/go-rewrite/compat-matrix.sh`                                                         | all pairs green, concurrent run clean                                                               |
| G5.11 | `bash scripts/go-rewrite/bench.sh --compare`                                                       | within thresholds in `benchmarks.md`                                                                |
| G5.12 | `govulncheck ./... && go test ./... -run Security`                                                 | no reachable vulns; security tests pass                                                             |
| G5.13 | `bash scripts/go-rewrite/doc-commands.sh && node scripts/go-rewrite/check-links.mjs`               | pass                                                                                                |
| G5.14 | `phase-5-acceptance.md`                                                                            | every item ticked on Linux, macOS, Windows                                                          |
| G5.15 | `test -s docs/specs/go-rewrite/phase-5-retire-node.md`                                             | proposal exists; no deletions in the diff (`git diff --stat` shows none under `packages/`, `apps/`) |
| G5.16 | `bash scripts/go-rewrite/check-file-lines.sh`                                                      | no Go file over 400 lines                                                                           |

## Rollback

- Server users: reinstall the previous Node release tarball (same layout, same unit name) or run the documented downgrade; data stays valid (verified by G5.7, G5.10).
- Docker: pin the previous image tag; volumes unchanged.
- Desktop (option A): the previous Desktop version bundles the Node agent; sessions remain readable (shared store).
- Remote hosts: both agents speak the same protocol; redeploy Node agent via the previous Desktop/server version.
- Keep the Node artifacts for one full release cycle (step 5.11 gates removal).

## Verify-first list

1. Which server targets `build-sea.mjs` and `release-server.yaml` actually build (the header lists darwin-arm64, linux-x64, linux-arm64) and whether Windows server artifacts exist.
2. What the old `nex update` validates in the manifest (`runtime/manifest.ts`), and whether the old CLI rejects a manifest without a Node runtime entry.
3. The exact command set and flags of `nex` (`packages/nex-server-cli/src/cli.ts`); service scope (user vs system units).
4. Whether Desktop imports remote deploy code from `@nex/server/remote/*` (the desktop host files listed import `@nex/services`; `@nex/server` usage was not checked).
5. `SERVER_RUNTIME_NODE_VERSION` 22.16.0 vs pinned 24.14.0.
6. Origin/CSRF policy for WebSocket upgrades in the Node server (none found in `http.ts`).
7. Code-signing/notarization requirements for a Go child binary inside the Desktop bundle.
8. Resolved: `NEX_VERSION` comes from the root `package.json` via a tsup define; confirm the Desktop build (`packages/desktop/tsup.config.ts`, which uses `buildMetadata.appVersion`) yields the same number for a given release.
9. Whether anything outside this repo (CDN manifests, install URLs, ghcr tags) depends on artifact names.
