# Changelog

All notable changes to Nex-v3 will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
