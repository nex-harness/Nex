# Changelog

All notable changes to Nex-v3 will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.4.1] - 2026-11-28

### Added
- **Desktop & Server Self-Updater**: Cross-platform GitHub Release auto-check, optional auto-download, skip-version persistence, and install prompt across `UpdateBanner`, `AboutModal`, and Settings update preferences.
- **macOS Release Code Signing & Notarization**: Added conditional Developer ID code signing and Apple Notarization (`NEX_ENABLE_MAC_SIGN`) to GitHub Release CI workflows.

### Changed
- **Versioned Release Artifacts**: Added `v${VERSION}` tags to all server tarballs, standalone runner/relay binaries, and desktop/mobile release asset filenames.
- **Runner & Update Version Badges**: Injected base SemVer + `buildSHA` in CI and formatted Runner and target update badges as `vX.Y.Z(sha)`.

### Fixed
- **MCP Credentials Vault & Workspace Dir**: Injected Nex Credentials Vault environment variables (`ResolveEffectiveEnv`) and workspace `cmd.Dir` into runner-local `.mcp.json` stdio MCP subprocesses.

## [0.4.0] - 2026-11-28

### Added
- **Autonomous Goal Mode**: Multi-round autonomous Goal Mode with independent verifier, `ConfigSnapshot` sync, inline `GoalDivider`, and Web UI controls.
- **Conversational `/schedule` Automations**: Natural-language `/schedule` creation, per-job model selection, manual test-run, and redesigned Automations Settings UI.
- **Auto-Discovered MCP & Lazy Catalog Hydration**: Automatic discovery of workspace `.mcp.json` and user `~/.nex/mcp.json` / `~/.claude/mcp.json` servers with hot merge, plus on-demand `ListTools` lazy hydration in `codemode` (`searchTools` / `describeTool`) and object-argument support.
- **Multi-Runner Sidebar & Appearance**: Runner filter chips with Morandi tinted or minimal border badge styles (configurable in Appearance), Active & Unread section, recency-sorted smart-collapsing projects, and time-bucketed Recent view.
- **Runner Rename & Delete Confirmation**: Sticky inline runner renaming (`PATCH /api/v1/runners/{id}`) preserved across reconnects, plus confirmation dialog before removing runners.
- **Usage Analytics Trend Chart**: Interactive hourly (`24h`) and daily (`7d` / `30d`) stacked Token / Cost / Turns SVG chart with token composition bar, hover inspector, and provider display names.
- **Update Check & Bilingual Docs**: Server & Desktop update check API integrated into the About modal, plus comprehensive English and Chinese READMEs (`README.md` and `README.zh-CN.md`).

### Changed
- **Default Permission Mode & Persistence**: Changed default permission mode to `interactive` (confirm before changes) and persisted user selection in `localStorage`.
- **Model & Provider UX**: Removed auto-seeded placeholder `anthropic`/`openai` providers on fresh startup, hid internal Provider ID input, removed redundant inline model rename button, distinguished Add vs Edit model modal titles, and exposed `PDF`, `Tools`, and `Prompt Cache` capability toggles.
- **Question Card UX**: Single-select options now auto-advance to the next question or auto-submit on the last question, and question prompts render only inside conversation views (not over Settings).
- **SSH Runner Deployment**: Unified runner connect actions inside modal and streamlined deployment to version-matched GitHub Release downloads (`remote_install` or local download + SCP).
- **Conversation Timeline**: Folded intermediate tool-calling narration into a single `WorkGroup` per turn, enabled editing sent messages in the main composer, and kept the abort button visible while running.

### Fixed
- **Relay `server_proxy` Routing**: Routed `server_proxy` LLM providers over the Runner's active `ServerWSURL` origin so E2EE relay runners work without direct LAN access.
- **Runner Self-Update & Desktop**: Bypassed lingering idle PTYs on manual runner upgrades, surfaced draining workload details, injected Desktop version into embedded server/runner, defaulted workspace to `$HOME`, and protected embedded runner from deletion.
- **Web UI & Input**: Fixed Tailwind v4 `.dark` mode contrast, prevented IME Enter confirmation from prematurely sending messages on Safari/WKWebView, deduplicated identical assistant `message_end` events, fixed absolute file preview paths with visible load/error states, fixed literal `{path}` placeholder in Global Instructions heading, and restored macOS titlebar double-click maximize.

## [0.3.0] - 2026-11-24

### Added
- **About Modal**: Added About Nex modal in the Help menu displaying brand logo, version, conditional debug build hash, and keyboard shortcuts.
- **Stream Resilience & Error Banner**: Added resettable stream idle timeout, zero-delta retry, and a compact ZCode-style stream error banner with Show details, Copy, and Dismiss controls.
- **Inline Compaction Divider & Context Meter Refresh**: Rendered inline collapsible compaction dividers in the conversation timeline and preserved post-compaction context breakdown, inline inspection, and turn tick rail cues.
- **Detached Background Tasks**: Decoupled background `bash` and subagents from foreground abort with per-task stop controls.
- **Agent & Tool Performance**: Capped large-window compaction thresholds, spilled `bash` output at 16KB, and accelerated `grep` using `rg` when available.
- **Web UI Ergonomics**: Auto-linked bare `http(s)://` URLs in Markdown rendering and collapsed thinking/tool execution details by default during streaming with a 1-line thinking preview.

### Changed
- **Docker Build Performance**: Switched to native `BUILDPLATFORM` Go cross-compilation, BuildKit GHA cache, `node:24-bookworm-slim` web-builder, and `.dockerignore` (reducing Docker CI build time by ~92%).
- **Branch Session Naming**: Prefixed forked session titles with `branch of ` instead of trailing `(branch)`.
- **Running Timer**: Displayed whole-second elapsed duration (`1s`, `2s`) instead of tenths of a second.

### Fixed
- **Orphaned Sessions & Subagent Abort**: Reconciled orphaned running sessions, cascaded abort to subagents, finalized child sessions before parent `agent_end`, and sealed dangling tool calls.
- **Runner Status & Auth**: Fixed stale runner online status, legacy update detection, succeeded badge TTL, and co-located local runner auth.
- **Telemetry & Reload Timer**: Ordered session turn telemetry chronologically by `recorded_at` and normalized event timestamps on reload so running timers do not reset.
- **Web UI**: Fixed Help menu outside-click handling so "About Nex" opens reliably, isolated composer image attachments per session, and removed accidental `$` prefix before skill names in Settings → Skills.
- **Skills Discovery**: Parsed YAML multiline block scalars (`|` and `>`) in `SKILL.md` frontmatter.
- **Build Scripts**: Used POSIX `wc -c` in `gen-runner-manifest.sh` for Alpine BusyBox compatibility.

## [0.2.0] - 2026-11-24

### Added
- **Image Generation & Classifier (Jev / System One)**:
  - Unified `ModelType` (`chat` / `image` / `classifier`), `ImageProvider` (OpenAI, OpenRouter, Grok/xAI, MiniMax), and `ClassifierProvider` (TypeSafe System One / local `llama.cpp`).
  - Built-in `generate_image` tool (text-to-image & reference editing, auto-saved to `.nex/generated/` with inline image rendering) and `codemode` `models.generateImages()` / `models.classify()` / `image()` bridge.
  - Multi-provider single-active profiles, connection tests (`POST /api/v1/image/test`, `POST /api/v1/classifier/test`), and Jev Guardrail / Auto Router toggles in Settings → Providers & Models.
- **Runner Self-Update & Windows Experience**:
  - Runner self-update engine with idle drain, watchdog rollback, cross-platform binary swap, compatibility gating, and batch upgrade UI.
  - Pure-Go Windows UAC elevation, Task Scheduler autostart, and Win32 system tray.
- **Runner Auth Hardening & Intranet Relay Tunnel**:
  - Persistent runner pairing auth, secret rotation API, paired-device token support, and `nex://pair` intranet & `nex-relay` E2EE tunnel connection.
- **Encrypted CLI Credentials Vault**:
  - Multi-runner encrypted CLI credentials vault, output redactor, and Settings management UI.
- **Global Instructions (`AGENTS.md`) Editor**:
  - `GET/PUT /api/v1/instructions/global` with Settings UI editor and automatic sync to Runners.
- **Conversation & UI Enhancements**:
  - Immediate mid-stream `Steer` interruption (`Loop.Steer` cancels active LLM stream and pivots immediately).
  - In-place message edit-and-resend (`POST /api/v1/sessions/{id}/truncate`), inline session title rename, and fullscreen image/PDF overlay modal with bottom zoom/rotate bar.
  - Inline `QuestionCard`, auto-growing composer, per-session `thinking_effort` isolation, HTTP gzip, and tail-window session rendering.

### Changed
- **Prompt Cache Optimization**: Removed volatile `Today's date` and `# gitStatus` from system prompts and reordered sections (`core` → `instructions` → `skills` → `mcp` → `environment`) for maximum prefix-cache hit rate.
- Renamed legacy `zcode-*` CSS classes/animations to `nex-*` and cleaned branding references from source.

### Fixed
- Auto-append `/v1` for bare-origin BaseURLs on image and classifier endpoints.
- Sync `MultimodalSettings` in `ConfigSnapshot` from Server to Runners.
- Preserve user-renamed session titles when ingesting runner `SessionMeta`.
- Decouple `ImagePreviewModal` image transform from dialog entrance animation so zoom and rotate controls work reliably.
- Mint short-lived download tokens for runner binary fallback URLs.

## [0.1.2] - 2026-11-23

### Fixed
- Desktop builds are split by runner: macOS `.dmg` now builds on `macos-latest`, Linux/Windows on `ubuntu-latest`, and Android APK/AAB on a runner with JDK 21 + Android SDK.
- App bundles (`mygo.json` / `clients/web/package.json`) now inherit the release version via `scripts/set-app-version.sh` instead of a hardcoded `0.3.0`.

### Added
- iOS Simulator `.app` is published as `nex-ios-simulator.app.zip`; a signed device IPA will be added once Apple signing credentials are available.
- `scripts/build-clients.sh` now supports per-platform targets (`desktop-macos`, `desktop-linux`, `desktop-windows`).
- Release job aggregates every build job's artifacts and publishes `latest.json` + `SHA256SUMS.txt` alongside them.

## [0.1.1] - 2026-11-23

### Added
- Dockerfiles for `nex-server` (`deploy/Dockerfile.server`) and `nex-relay` (`deploy/Dockerfile.relay`) with multi-arch (`linux/amd64`, `linux/arm64`) GHCR publishing.
- `-version` flag and `ldflags` version injection for `nex-server` and `nex-relay`.
- `version.json` in packaged server distributions and `latest.json` update manifest in release artifacts for in-app update checks.
- Desktop (macOS/Linux/Windows) and mobile (Android APK / iOS IPA) build jobs in GitHub Actions workflow.
- `CHANGELOG.md` release history.

### Fixed
- Docker builds now set `GOTOOLCHAIN=auto` so the Go version required by `go.mod` is used automatically.

## [0.1.0] - 2026-11-23

### Added
- Remote Access: system Tailscale probe, on-demand `nex-tsnet` sidecar, relay screencast adaptation, sleep guard, E2EE multi-path tunnel proxy, offline QR pairing, and paired host switcher.
- Web UI: ZCode-aligned chat loading indicator (`ChatLoading`), Settings UI, Markdown export, Cmd+K command palette, full-text session search, and turn checkpoint/rewind.
- Capabilities: Plan mode approval loop, built-in `websearch`, pure-Go PDF reading, persistent memory system, Skills discovery, and MCP manager.
- Multi-platform `nex-server` packaging and `nex-runner` / `nex-relay` binaries.
