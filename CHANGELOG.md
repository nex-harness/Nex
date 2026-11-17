# Changelog

All notable changes to Nex are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [2.0.0] - 2026-11-18

Rebased on upstream ZCode v3.18.2 and re-applied the Nex fork's changes on top
of it. This release drops the account/commercial layer entirely, so it is a
breaking major version.

### Changed

- Rebased on ZCode v3.18.2 (previously v3.14.3). This brings the upstream R3
  Source Server remote architecture, the Project/Task sidebar model, composer
  edit mode, managed worktrees, extension host and app-control navigation,
  voice input (without the login gate), tool search, data sync, the plugin UI
  host, Japanese/Korean/Russian UI locales, and the file-menu / image-editor
  surfaces.
- ZCode → Nex identity layer re-applied to the new baseline: `@nex/*` packages,
  `apps/nex-cli`, `NEX_*` environment variables, `nex://` URL scheme,
  `window.nex` preload bridge, `.nex-plugin` plugin directories, and the
  `~/.nex` data root.
- Model provider setup now looks models up on models.dev instead of the old
  smart-config rules: the provider form fills itself from a models.dev match,
  a manual fetch button drives the lookup, and `/v1/models` results can be
  batch-added from a picker that marks already-configured models.

### Removed

- All telemetry reporting: ARMS/RUM, OpenTelemetry OTLP exporters, the
  telemetry collector pipelines in desktop main/host/renderer, the
  `@nex/telemetry` workspace package, and every telemetry environment key.
  Nothing leaves the machine. In-app usage statistics, token counts and the MCP
  resource sampler stay, because they never make a network request.
- Account login: OAuth sign-in, JWT session handling, the login gate,
  onboarding gate and the account-gated desktop deep links.
- Coding Plan: subscription providers, quota reset UI, plan badges and the
  billing webview.
- Off-peak (idle-time) task dispatch and its desktop scheduler wiring.
- The built-in provider catalog. Providers are now always user-defined; the
  wildcard validation baseline is kept so personal model entries still resolve.
- The vendor's default plugin marketplace.

### Added

- Standalone server web login and static web hosting (`nex serve --web-root`,
  `nex web-token`) with direct-server `hostId` mapping.
- Desktop updates and remote server release assets distributed via GitHub
  Releases (`v*` tags), with local-artifact deployment support for testing.
- Docker image and compose file for a single-container server deployment.
- GitHub Actions workflows: PR checks, server release, desktop release, and
  image publishing.
